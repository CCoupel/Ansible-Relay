package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"secagent-server/cmd/secagent-server/internal/storage"
	"secagent-server/cmd/secagent-server/internal/ws"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

func newRouterTestStore(t *testing.T) *storage.Store {
	t.Helper()
	s, err := storage.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// seedPushRelay inserts a push-mode relay node and routing entry.
func seedPushRelay(t *testing.T, s *storage.Store, relayID, url string, hostnames []string) {
	t.Helper()
	node := storage.RelayNode{
		ID:        "uuid-" + relayID,
		RelayID:   relayID,
		URL:       url,
		TokenHash: "test-push-token",
		Mode:      "push",
		Status:    "connected",
		CreatedAt: time.Now().Unix(),
	}
	if err := s.UpsertRelayNode(node); err != nil {
		t.Fatalf("UpsertRelayNode: %v", err)
	}
	if err := s.BulkUpsertRelayRouting(relayID, hostnames); err != nil {
		t.Fatalf("BulkUpsertRelayRouting: %v", err)
	}
}

// mockExecRelay starts a test relay server that accepts exec requests.
func mockExecRelay(t *testing.T, rc int, stdout string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/exec/") {
			json.NewEncoder(w).Encode(ExecResponse{RC: rc, Stdout: stdout}) //nolint:errcheck
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/upload/") {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/fetch/") {
			json.NewEncoder(w).Encode(FetchResponse{RC: 0, Data: "dGVzdA=="}) //nolint:errcheck
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ── GetRelayForHostname ────────────────────────────────────────────────────────

func TestProxyRouter_GetRelayForHostname_Found(t *testing.T) {
	s := newRouterTestStore(t)
	seedPushRelay(t, s, "dmz1", "http://dmz1:7770", []string{"host-a", "host-b"})

	r := NewProxyRouter(s)
	relayID, err := r.GetRelayForHostname("host-a")
	if err != nil {
		t.Fatalf("GetRelayForHostname: %v", err)
	}
	if relayID != "dmz1" {
		t.Errorf("expected dmz1, got %q", relayID)
	}
}

func TestProxyRouter_GetRelayForHostname_NotFound(t *testing.T) {
	s := newRouterTestStore(t)
	r := NewProxyRouter(s)

	_, err := r.GetRelayForHostname("unknown-host")
	if err == nil {
		t.Fatal("expected ErrHostNotFound")
	}
	if err != ErrHostNotFound {
		t.Errorf("expected ErrHostNotFound, got %v", err)
	}
}

func TestProxyRouter_GetRelayForHostname_MultipleRelays(t *testing.T) {
	s := newRouterTestStore(t)
	seedPushRelay(t, s, "dmz1", "http://dmz1:7770", []string{"host-a", "host-b"})
	seedPushRelay(t, s, "dmz2", "http://dmz2:7770", []string{"host-c", "host-d"})

	r := NewProxyRouter(s)

	for _, tc := range []struct{ host, want string }{
		{"host-a", "dmz1"},
		{"host-b", "dmz1"},
		{"host-c", "dmz2"},
		{"host-d", "dmz2"},
	} {
		rid, err := r.GetRelayForHostname(tc.host)
		if err != nil {
			t.Errorf("GetRelayForHostname(%q): %v", tc.host, err)
			continue
		}
		if rid != tc.want {
			t.Errorf("host %q → relay %q, want %q", tc.host, rid, tc.want)
		}
	}
}

// ── RouteExec push mode ───────────────────────────────────────────────────────

func TestProxyRouter_RouteExec_PushMode_Success(t *testing.T) {
	srv := mockExecRelay(t, 0, "hello from relay")
	s := newRouterTestStore(t)
	seedPushRelay(t, s, "dmz-push", srv.URL, []string{"push-host"})

	r := NewProxyRouter(s)
	r.isRelayConnected = func(string) bool { return false } // force push mode

	resp, err := r.RouteExec(context.Background(), "push-host", "task-1", ExecRequest{
		Cmd: "echo hi", Timeout: 10,
	})
	if err != nil {
		t.Fatalf("RouteExec: %v", err)
	}
	if resp.RC != 0 {
		t.Errorf("expected RC=0, got %d", resp.RC)
	}
	if resp.Stdout != "hello from relay\n" && resp.Stdout != "hello from relay" {
		t.Errorf("unexpected stdout: %q", resp.Stdout)
	}
}

func TestProxyRouter_RouteExec_HostNotFound(t *testing.T) {
	s := newRouterTestStore(t)
	r := NewProxyRouter(s)

	_, err := r.RouteExec(context.Background(), "no-such-host", "task-x", ExecRequest{Cmd: "ls"})
	if err != ErrHostNotFound {
		t.Errorf("expected ErrHostNotFound, got %v", err)
	}
}

func TestProxyRouter_RouteExec_RelayOffline(t *testing.T) {
	s := newRouterTestStore(t)
	// Register relay in DB but no URL (won't get push client) and not WS connected
	node := storage.RelayNode{
		ID: "uuid-offline", RelayID: "offline-relay",
		URL: "", Mode: "pull", Status: "disconnected", CreatedAt: time.Now().Unix(),
	}
	_ = s.UpsertRelayNode(node)
	_ = s.BulkUpsertRelayRouting("offline-relay", []string{"host-offline"})

	r := NewProxyRouter(s)
	r.isRelayConnected = func(string) bool { return false }

	_, err := r.RouteExec(context.Background(), "host-offline", "task-y", ExecRequest{Cmd: "ls"})
	if err == nil {
		t.Fatal("expected error for offline relay")
	}
}

// ── RouteExec pull mode ───────────────────────────────────────────────────────

func TestProxyRouter_RouteExec_PullMode_Success(t *testing.T) {
	s := newRouterTestStore(t)
	node := storage.RelayNode{
		ID: "uuid-pull", RelayID: "dmz-pull",
		Mode: "pull", Status: "connected", CreatedAt: time.Now().Unix(),
	}
	_ = s.UpsertRelayNode(node)
	_ = s.BulkUpsertRelayRouting("dmz-pull", []string{"pull-host"})

	r := NewProxyRouter(s)
	r.isRelayConnected = func(id string) bool { return id == "dmz-pull" }

	// Mock dispatch: immediately returns success result
	r.dispatchToRelay = func(relayID string, msg ws.RelayMessage) (chan ws.RelayTaskResult, error) {
		ch := make(chan ws.RelayTaskResult, 1)
		ch <- ws.RelayTaskResult{TaskID: msg.TaskID, RC: 0, Stdout: "pull output"}
		return ch, nil
	}
	r.unregisterRelayFuture = func(string) {} // no-op

	resp, err := r.RouteExec(context.Background(), "pull-host", "task-pull-1", ExecRequest{
		Cmd: "echo hi", Timeout: 5,
	})
	if err != nil {
		t.Fatalf("RouteExec pull: %v", err)
	}
	if resp.Stdout != "pull output" {
		t.Errorf("unexpected stdout: %q", resp.Stdout)
	}
}

func TestProxyRouter_RouteExec_PullMode_RelayDisconnect(t *testing.T) {
	s := newRouterTestStore(t)
	node := storage.RelayNode{
		ID: "uuid-pull-dc", RelayID: "dmz-pull-dc",
		Mode: "pull", Status: "connected", CreatedAt: time.Now().Unix(),
	}
	_ = s.UpsertRelayNode(node)
	_ = s.BulkUpsertRelayRouting("dmz-pull-dc", []string{"host-dc"})

	r := NewProxyRouter(s)
	r.isRelayConnected = func(id string) bool { return id == "dmz-pull-dc" }

	// Mock dispatch: returns relay_disconnected error
	r.dispatchToRelay = func(relayID string, msg ws.RelayMessage) (chan ws.RelayTaskResult, error) {
		ch := make(chan ws.RelayTaskResult, 1)
		ch <- ws.RelayTaskResult{TaskID: msg.TaskID, Error: "relay_disconnected"}
		return ch, nil
	}
	r.unregisterRelayFuture = func(string) {}

	_, err := r.RouteExec(context.Background(), "host-dc", "task-dc", ExecRequest{Cmd: "ls", Timeout: 5})
	if err == nil {
		t.Fatal("expected error for relay_disconnected")
	}
	if !containsStr(err.Error(), "relay_disconnected") {
		t.Errorf("expected relay_disconnected error, got %v", err)
	}
}

// ── RouteUpload ───────────────────────────────────────────────────────────────

func TestProxyRouter_RouteUpload_PushMode_Success(t *testing.T) {
	srv := mockExecRelay(t, 0, "")
	s := newRouterTestStore(t)
	seedPushRelay(t, s, "dmz-up", srv.URL, []string{"up-host"})

	r := NewProxyRouter(s)
	r.isRelayConnected = func(string) bool { return false }

	err := r.RouteUpload(context.Background(), "up-host", "task-up", UploadRequest{
		Dest: "/tmp/f.txt", Data: "aGVsbG8=", Mode: "0644",
	})
	if err != nil {
		t.Fatalf("RouteUpload: %v", err)
	}
}

func TestProxyRouter_RouteUpload_HostNotFound(t *testing.T) {
	s := newRouterTestStore(t)
	r := NewProxyRouter(s)

	err := r.RouteUpload(context.Background(), "no-host", "t-up", UploadRequest{Dest: "/x"})
	if err != ErrHostNotFound {
		t.Errorf("expected ErrHostNotFound, got %v", err)
	}
}

// ── RouteFetch ────────────────────────────────────────────────────────────────

func TestProxyRouter_RouteFetch_PushMode_Success(t *testing.T) {
	srv := mockExecRelay(t, 0, "")
	s := newRouterTestStore(t)
	seedPushRelay(t, s, "dmz-ft", srv.URL, []string{"ft-host"})

	r := NewProxyRouter(s)
	r.isRelayConnected = func(string) bool { return false }

	resp, err := r.RouteFetch(context.Background(), "ft-host", "task-ft", FetchRequest{Src: "/etc/h"})
	if err != nil {
		t.Fatalf("RouteFetch: %v", err)
	}
	if resp.Data == "" {
		t.Error("expected non-empty data")
	}
}

func TestProxyRouter_RouteFetch_HostNotFound(t *testing.T) {
	s := newRouterTestStore(t)
	r := NewProxyRouter(s)

	_, err := r.RouteFetch(context.Background(), "no-host", "t-ft", FetchRequest{Src: "/x"})
	if err != ErrHostNotFound {
		t.Errorf("expected ErrHostNotFound, got %v", err)
	}
}

// ── AggregateRelayInventory ───────────────────────────────────────────────────

func TestProxyRouter_AggregateRelayInventory_Empty(t *testing.T) {
	s := newRouterTestStore(t)
	r := NewProxyRouter(s)
	r.isRelayConnected = func(string) bool { return false }

	entries, err := r.AggregateRelayInventory()
	if err != nil {
		t.Fatalf("AggregateRelayInventory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestProxyRouter_AggregateRelayInventory_TwoRelays(t *testing.T) {
	s := newRouterTestStore(t)
	seedPushRelay(t, s, "dmz1", "http://dmz1:7770", []string{"h1", "h2", "h3"})
	seedPushRelay(t, s, "dmz2", "http://dmz2:7770", []string{"h4", "h5", "h6"})

	r := NewProxyRouter(s)
	r.isRelayConnected = func(id string) bool { return id == "dmz1" } // dmz1 live

	entries, err := r.AggregateRelayInventory()
	if err != nil {
		t.Fatalf("AggregateRelayInventory: %v", err)
	}
	if len(entries) != 6 {
		t.Fatalf("expected 6 entries, got %d", len(entries))
	}

	// Count by relay
	cnt := make(map[string]int)
	for _, e := range entries {
		cnt[e.RelayID]++
	}
	if cnt["dmz1"] != 3 {
		t.Errorf("expected 3 from dmz1, got %d", cnt["dmz1"])
	}
	if cnt["dmz2"] != 3 {
		t.Errorf("expected 3 from dmz2, got %d", cnt["dmz2"])
	}

	// dmz1 should be "connected" (live WS), dmz2 "connected" (DB seeded as connected)
	for _, e := range entries {
		if e.RelayID == "dmz1" && e.RelayStatus != "connected" {
			t.Errorf("dmz1 agent %q: expected connected, got %q", e.Hostname, e.RelayStatus)
		}
	}
}

func TestProxyRouter_AggregateRelayInventory_DisconnectedRelay(t *testing.T) {
	s := newRouterTestStore(t)
	node := storage.RelayNode{
		ID: "uuid-dc", RelayID: "dmz-dc",
		URL: "", Mode: "pull", Status: "disconnected", CreatedAt: time.Now().Unix(),
	}
	_ = s.UpsertRelayNode(node)
	_ = s.BulkUpsertRelayRouting("dmz-dc", []string{"dc-host-1", "dc-host-2"})

	r := NewProxyRouter(s)
	r.isRelayConnected = func(string) bool { return false }

	entries, err := r.AggregateRelayInventory()
	if err != nil {
		t.Fatalf("AggregateRelayInventory: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	for _, e := range entries {
		if e.RelayStatus != "disconnected" {
			t.Errorf("expected disconnected for dc relay agent %q, got %q", e.Hostname, e.RelayStatus)
		}
		if e.RelayID != "dmz-dc" {
			t.Errorf("expected RelayID=dmz-dc, got %q", e.RelayID)
		}
	}
}

// ── getPushClient caching ─────────────────────────────────────────────────────

func TestProxyRouter_PushClientCached(t *testing.T) {
	srv := mockExecRelay(t, 0, "ok")
	s := newRouterTestStore(t)
	seedPushRelay(t, s, "cached-relay", srv.URL, []string{"cached-host"})

	r := NewProxyRouter(s)
	r.isRelayConnected = func(string) bool { return false }

	c1, err := r.getPushClient("cached-relay")
	if err != nil {
		t.Fatalf("getPushClient: %v", err)
	}
	c2, _ := r.getPushClient("cached-relay")
	if c1 != c2 {
		t.Error("expected same client instance on second call (cache)")
	}
}

func TestProxyRouter_PushClientNotFound(t *testing.T) {
	s := newRouterTestStore(t)
	r := NewProxyRouter(s)

	_, err := r.getPushClient("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent relay")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func containsStr(s, sub string) bool {
	return strings.Contains(s, sub)
}
