// chaining_test.go — Tests for proxy chaining and anti-loop hop counting.
//
// Validates:
//   - WithRelayHops / RelayHopsFromContext context helpers
//   - RelayClient propagates X-Relay-Hops header in outgoing requests
//   - Three-level push chain: proxy-A (ProxyRouter) → proxy-B (httptest) → relay-C (httptest)
//   - Loop detection: relay returns 508, error is propagated to caller
package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// ── Context helpers ───────────────────────────────────────────────────────────

func TestWithRelayHops_DefaultWhenAbsent(t *testing.T) {
	got := RelayHopsFromContext(context.Background())
	if got != DefaultMaxHops {
		t.Errorf("expected DefaultMaxHops=%d, got %d", DefaultMaxHops, got)
	}
}

func TestWithRelayHops_Roundtrip(t *testing.T) {
	for _, hops := range []int{0, 1, 3, DefaultMaxHops, 100} {
		ctx := WithRelayHops(context.Background(), hops)
		got := RelayHopsFromContext(ctx)
		if got != hops {
			t.Errorf("hops=%d: roundtrip got %d", hops, got)
		}
	}
}

func TestWithRelayHops_ParentNotAffected(t *testing.T) {
	parent := context.Background()
	_ = WithRelayHops(parent, 3)
	// parent should still return DefaultMaxHops
	if got := RelayHopsFromContext(parent); got != DefaultMaxHops {
		t.Errorf("parent context was mutated: expected %d, got %d", DefaultMaxHops, got)
	}
}

// ── RelayClient hop header propagation ───────────────────────────────────────

func TestRelayClient_SetsHopsHeader_FromContext(t *testing.T) {
	var capturedHops string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHops = r.Header.Get(RelayHopsHeader)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ExecResponse{RC: 0, Stdout: "ok"}) //nolint:errcheck
	}))
	defer srv.Close()

	client := NewRelayClient("test-relay", srv.URL, "token")
	ctx := WithRelayHops(context.Background(), 5)
	_, err := client.Exec(ctx, "some-host", ExecRequest{Cmd: "ls", Timeout: 5})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if capturedHops != "5" {
		t.Errorf("expected X-Relay-Hops=5, got %q", capturedHops)
	}
}

func TestRelayClient_SetsHopsHeader_DefaultWhenNoContext(t *testing.T) {
	var capturedHops string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHops = r.Header.Get(RelayHopsHeader)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ExecResponse{RC: 0, Stdout: "ok"}) //nolint:errcheck
	}))
	defer srv.Close()

	client := NewRelayClient("test-relay", srv.URL, "token")
	_, err := client.Exec(context.Background(), "some-host", ExecRequest{Cmd: "ls", Timeout: 5})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	expected := strconv.Itoa(DefaultMaxHops)
	if capturedHops != expected {
		t.Errorf("expected X-Relay-Hops=%s, got %q", expected, capturedHops)
	}
}

func TestRelayClient_GetInventory_SetsHopsHeader(t *testing.T) {
	var capturedHops string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedHops = r.Header.Get(RelayHopsHeader)
		w.Header().Set("Content-Type", "application/json")
		// Return minimal inventory
		json.NewEncoder(w).Encode(map[string]interface{}{ //nolint:errcheck
			"all": map[string]interface{}{"hosts": []string{}},
			"_meta": map[string]interface{}{"hostvars": map[string]interface{}{}},
		})
	}))
	defer srv.Close()

	client := NewRelayClient("inv-relay", srv.URL, "token")
	ctx := WithRelayHops(context.Background(), 3)
	_, err := client.GetInventory(ctx)
	if err != nil {
		t.Fatalf("GetInventory: %v", err)
	}
	if capturedHops != "3" {
		t.Errorf("expected X-Relay-Hops=3, got %q", capturedHops)
	}
}

// ── Three-level push chain ─────────────────────────────────────────────────────
//
// Topology: proxyA (ProxyRouter) → proxyB (httptest) → relayC (httptest)
// Verifies that exec result from relayC propagates back to proxyA.

func TestProxyChaining_ThreeLevel_Exec(t *testing.T) {
	// relayC: terminal relay — returns "relay-c-output"
	relayCServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/exec/") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		// Honour loop detection header
		if hopsStr := r.Header.Get(RelayHopsHeader); hopsStr != "" {
			if n, _ := strconv.Atoi(hopsStr); n <= 0 {
				w.WriteHeader(http.StatusLoopDetected)
				json.NewEncoder(w).Encode(map[string]string{"error": "relay_loop_detected"}) //nolint:errcheck
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ExecResponse{RC: 0, Stdout: "relay-c-output"}) //nolint:errcheck
	}))
	defer relayCServer.Close()

	// proxyB: intermediate proxy that forwards to relayC with decremented hops
	relayCURL := relayCServer.URL
	proxyBServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/exec/") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		// Enforce hop budget
		hops := DefaultMaxHops
		if hopsStr := r.Header.Get(RelayHopsHeader); hopsStr != "" {
			n, _ := strconv.Atoi(hopsStr)
			if n <= 0 {
				w.WriteHeader(http.StatusLoopDetected)
				json.NewEncoder(w).Encode(map[string]string{"error": "relay_loop_detected"}) //nolint:errcheck
				return
			}
			hops = n - 1
		}
		// Forward to relayC
		hostname := strings.TrimPrefix(r.URL.Path, "/api/exec/")
		var body ExecRequest
		_ = json.NewDecoder(r.Body).Decode(&body)

		clientC := NewRelayClient("relay-c", relayCURL, "c-token")
		ctx := WithRelayHops(r.Context(), hops)
		resp, err := clientC.Exec(ctx, hostname, body)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()}) //nolint:errcheck
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer proxyBServer.Close()

	// proxyA: ProxyRouter with a push relay entry pointing to proxyB
	s := newRouterTestStore(t)
	seedPushRelay(t, s, "proxy-b", proxyBServer.URL, []string{"host-c"})

	routerA := &ProxyRouter{
		store:                 s,
		pushClients:           make(map[string]*RelayClient),
		isRelayConnected:      func(string) bool { return false }, // push mode
		dispatchToRelay:       nil,
		unregisterRelayFuture: nil,
	}

	resp, err := routerA.RouteExec(context.Background(), "host-c", "task-chain-1", ExecRequest{
		Cmd:     "echo chain",
		Timeout: 10,
	})
	if err != nil {
		t.Fatalf("RouteExec chain error: %v", err)
	}
	// Allow for trailing newline variations from JSON encoding
	got := strings.TrimSpace(resp.Stdout)
	if got != "relay-c-output" {
		t.Errorf("expected stdout=relay-c-output, got %q", resp.Stdout)
	}
	if resp.RC != 0 {
		t.Errorf("expected RC=0, got %d", resp.RC)
	}
}

// TestProxyChaining_HopsDecrement verifies that the hop count decreases correctly
// through a two-node chain (proxy-A sends N, intermediate receives N and forwards N-1).
func TestProxyChaining_HopsDecrement(t *testing.T) {
	// Record hop values at each node
	var receivedByIntermediate, forwardedToFinal string

	// final node: just records what it receives
	finalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwardedToFinal = r.Header.Get(RelayHopsHeader)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ExecResponse{RC: 0, Stdout: "final"}) //nolint:errcheck
	}))
	defer finalServer.Close()

	finalURL := finalServer.URL
	// intermediate: records received, forwards -1
	intermediateServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedByIntermediate = r.Header.Get(RelayHopsHeader)
		hops := DefaultMaxHops
		if n, err := strconv.Atoi(receivedByIntermediate); err == nil {
			hops = n - 1
		}
		hostname := strings.TrimPrefix(r.URL.Path, "/api/exec/")
		var body ExecRequest
		_ = json.NewDecoder(r.Body).Decode(&body)

		clientFinal := NewRelayClient("final", finalURL, "t")
		ctx := WithRelayHops(r.Context(), hops)
		resp, err := clientFinal.Exec(ctx, hostname, body)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp) //nolint:errcheck
	}))
	defer intermediateServer.Close()

	s := newRouterTestStore(t)
	seedPushRelay(t, s, "intermediate", intermediateServer.URL, []string{"hop-host"})

	router := &ProxyRouter{
		store:                 s,
		pushClients:           make(map[string]*RelayClient),
		isRelayConnected:      func(string) bool { return false },
		dispatchToRelay:       nil,
		unregisterRelayFuture: nil,
	}

	// No hops in context → RelayClient uses DefaultMaxHops
	_, err := router.RouteExec(context.Background(), "hop-host", "task-hops", ExecRequest{
		Cmd: "ls", Timeout: 5,
	})
	if err != nil {
		t.Fatalf("RouteExec: %v", err)
	}

	expectedIntermediate := strconv.Itoa(DefaultMaxHops)
	if receivedByIntermediate != expectedIntermediate {
		t.Errorf("intermediate received X-Relay-Hops=%q, expected %q",
			receivedByIntermediate, expectedIntermediate)
	}
	expectedFinal := strconv.Itoa(DefaultMaxHops - 1)
	if forwardedToFinal != expectedFinal {
		t.Errorf("final received X-Relay-Hops=%q, expected %q", forwardedToFinal, expectedFinal)
	}
}

// ── Loop detection ────────────────────────────────────────────────────────────

// TestProxyChaining_LoopDetection_DownstreamReturns508 verifies that when a downstream
// relay/proxy returns HTTP 508 (loop detected), the ProxyRouter propagates the error.
func TestProxyChaining_LoopDetection_DownstreamReturns508(t *testing.T) {
	// Mock relay always responding 508
	loopSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusLoopDetected)
		json.NewEncoder(w).Encode(map[string]string{"error": "relay_loop_detected"}) //nolint:errcheck
	}))
	defer loopSrv.Close()

	s := newRouterTestStore(t)
	seedPushRelay(t, s, "loop-relay", loopSrv.URL, []string{"loop-host"})

	router := &ProxyRouter{
		store:                 s,
		pushClients:           make(map[string]*RelayClient),
		isRelayConnected:      func(string) bool { return false },
		dispatchToRelay:       nil,
		unregisterRelayFuture: nil,
	}

	_, err := router.RouteExec(context.Background(), "loop-host", "task-loop", ExecRequest{
		Cmd: "ls", Timeout: 5,
	})
	if err == nil {
		t.Fatal("expected error when downstream returns 508, got nil")
	}
	t.Logf("loop detection propagated error: %v", err)
}

// TestProxyChaining_LoopDetection_ZeroHopsContext verifies that when context hops=0
// is passed to RouteExec, the downstream relay receives X-Relay-Hops: 0 and rejects.
func TestProxyChaining_LoopDetection_ZeroHopsContext(t *testing.T) {
	var receivedHops string
	relaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHops = r.Header.Get(RelayHopsHeader)
		// Simulate relay enforcing loop detection
		if n, _ := strconv.Atoi(receivedHops); n <= 0 {
			w.WriteHeader(http.StatusLoopDetected)
			json.NewEncoder(w).Encode(map[string]string{"error": "relay_loop_detected"}) //nolint:errcheck
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ExecResponse{RC: 0, Stdout: "ok"}) //nolint:errcheck
	}))
	defer relaySrv.Close()

	s := newRouterTestStore(t)
	seedPushRelay(t, s, "ttl-relay", relaySrv.URL, []string{"ttl-host"})

	router := &ProxyRouter{
		store:                 s,
		pushClients:           make(map[string]*RelayClient),
		isRelayConnected:      func(string) bool { return false },
		dispatchToRelay:       nil,
		unregisterRelayFuture: nil,
	}

	// Context with 0 hops → RelayClient sets X-Relay-Hops: 0 → relay rejects
	ctx := WithRelayHops(context.Background(), 0)
	_, err := router.RouteExec(ctx, "ttl-host", "task-ttl", ExecRequest{Cmd: "ls", Timeout: 5})
	if err == nil {
		t.Fatal("expected error when hops=0, got nil")
	}
	if receivedHops != "0" {
		t.Errorf("expected relay to receive X-Relay-Hops=0, got %q", receivedHops)
	}
}
