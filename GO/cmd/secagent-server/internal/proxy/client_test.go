package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"secagent-server/cmd/secagent-server/internal/storage"
)

// ── Mock relay server ─────────────────────────────────────────────────────────

// mockRelay builds an httptest.Server that mimics the relay REST API.
// handlers is a map of "METHOD /path" → handler func.
func mockRelay(t *testing.T, handlers map[string]http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	for pattern, h := range handlers {
		parts := strings.SplitN(pattern, " ", 2)
		method, path := parts[0], parts[1]
		localMethod, localPath, localH := method, path, h
		mux.HandleFunc(localPath, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != localMethod {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			localH(w, r)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// jsonResponse writes a JSON-encoded v with the given status code.
func jsonResponse(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}

// ── GetInventory ──────────────────────────────────────────────────────────────

func TestRelayClient_GetInventory_Success(t *testing.T) {
	srv := mockRelay(t, map[string]http.HandlerFunc{
		"GET /api/inventory": func(w http.ResponseWriter, r *http.Request) {
			// Verify Bearer token
			if r.Header.Get("Authorization") != "Bearer test-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			jsonResponse(w, 200, map[string]interface{}{
				"all": map[string]interface{}{
					"hosts": []string{"host-a", "host-b", "host-c"},
				},
				"_meta": map[string]interface{}{
					"hostvars": map[string]interface{}{
						"host-a": map[string]interface{}{
							"secagent_status":    "connected",
							"secagent_last_seen": "2026-01-01T00:00:00Z",
						},
						"host-b": map[string]interface{}{
							"secagent_status": "disconnected",
						},
						"host-c": map[string]interface{}{},
					},
				},
			})
		},
	})

	client := NewRelayClient("dmz1", srv.URL, "test-token")
	agents, err := client.GetInventory(context.Background())
	if err != nil {
		t.Fatalf("GetInventory: %v", err)
	}
	if len(agents) != 3 {
		t.Fatalf("expected 3 agents, got %d", len(agents))
	}
	// Find host-a
	var found *InventoryAgent
	for _, a := range agents {
		if a.Hostname == "host-a" {
			aa := a
			found = &aa
			break
		}
	}
	if found == nil {
		t.Fatal("expected host-a in inventory")
	}
	if found.Status != "connected" {
		t.Errorf("expected host-a status=connected, got %s", found.Status)
	}
	if found.LastSeen != "2026-01-01T00:00:00Z" {
		t.Errorf("expected last_seen set, got %q", found.LastSeen)
	}
}

func TestRelayClient_GetInventory_UnauthorizedToken(t *testing.T) {
	srv := mockRelay(t, map[string]http.HandlerFunc{
		"GET /api/inventory": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		},
	})

	client := NewRelayClient("dmz1", srv.URL, "wrong-token")
	_, err := client.GetInventory(context.Background())
	if err == nil {
		t.Error("expected error for unauthorized token")
	}
}

func TestRelayClient_GetInventory_EmptyInventory(t *testing.T) {
	srv := mockRelay(t, map[string]http.HandlerFunc{
		"GET /api/inventory": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]interface{}{
				"all":   map[string]interface{}{"hosts": []string{}},
				"_meta": map[string]interface{}{"hostvars": map[string]interface{}{}},
			})
		},
	})

	client := NewRelayClient("dmz1", srv.URL, "tok")
	agents, err := client.GetInventory(context.Background())
	if err != nil {
		t.Fatalf("GetInventory: %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agents))
	}
}

func TestRelayClient_GetInventory_ServerDown(t *testing.T) {
	// Point to a non-existent server
	client := NewRelayClient("dmz1", "http://localhost:1", "tok")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := client.GetInventory(ctx)
	if err == nil {
		t.Error("expected error when relay is unreachable")
	}
}

func TestRelayClient_GetInventory_InvalidJSON(t *testing.T) {
	srv := mockRelay(t, map[string]http.HandlerFunc{
		"GET /api/inventory": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(200)
			w.Write([]byte("not-valid-json")) //nolint:errcheck
		},
	})

	client := NewRelayClient("dmz1", srv.URL, "tok")
	_, err := client.GetInventory(context.Background())
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

// ── Exec ─────────────────────────────────────────────────────────────────────

func TestRelayClient_Exec_Success(t *testing.T) {
	srv := mockRelay(t, map[string]http.HandlerFunc{
		"POST /api/exec/host-a": func(w http.ResponseWriter, r *http.Request) {
			// Verify auth
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// Decode request
			var req ExecRequest
			json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
			jsonResponse(w, 200, ExecResponse{
				TaskID: "task-1",
				RC:     0,
				Stdout: fmt.Sprintf("ran: %s", req.Cmd),
			})
		},
	})

	client := NewRelayClient("dmz1", srv.URL, "my-token")
	resp, err := client.Exec(context.Background(), "host-a", ExecRequest{
		Cmd: "echo hello", Timeout: 10,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if resp.RC != 0 {
		t.Errorf("expected rc=0, got %d", resp.RC)
	}
	if !strings.Contains(resp.Stdout, "echo hello") {
		t.Errorf("unexpected stdout: %q", resp.Stdout)
	}
}

func TestRelayClient_Exec_AllFields(t *testing.T) {
	var capturedReq ExecRequest
	srv := mockRelay(t, map[string]http.HandlerFunc{
		"POST /api/exec/target": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&capturedReq) //nolint:errcheck
			jsonResponse(w, 200, ExecResponse{RC: 0})
		},
	})

	client := NewRelayClient("dmz1", srv.URL, "tok")
	_, err := client.Exec(context.Background(), "target", ExecRequest{
		Cmd:          "ls /",
		Stdin:        "input-data",
		Timeout:      30,
		Become:       true,
		BecomeMethod: "sudo",
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if capturedReq.Cmd != "ls /" {
		t.Errorf("cmd not transmitted: %q", capturedReq.Cmd)
	}
	if !capturedReq.Become {
		t.Error("become flag not transmitted")
	}
	if capturedReq.BecomeMethod != "sudo" {
		t.Errorf("become_method not transmitted: %q", capturedReq.BecomeMethod)
	}
	if capturedReq.Timeout != 30 {
		t.Errorf("timeout not transmitted: %d", capturedReq.Timeout)
	}
}

func TestRelayClient_Exec_NotFound(t *testing.T) {
	srv := mockRelay(t, map[string]http.HandlerFunc{
		"POST /api/exec/missing-host": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 404, map[string]string{"error": "agent_offline"})
		},
	})

	client := NewRelayClient("dmz1", srv.URL, "tok")
	_, err := client.Exec(context.Background(), "missing-host", ExecRequest{Cmd: "ls"})
	if err == nil {
		t.Error("expected error for 404 response")
	}
}

// ── Upload ────────────────────────────────────────────────────────────────────

func TestRelayClient_Upload_Success(t *testing.T) {
	var capturedReq UploadRequest
	srv := mockRelay(t, map[string]http.HandlerFunc{
		"POST /api/upload/host-b": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&capturedReq) //nolint:errcheck
			jsonResponse(w, 200, map[string]string{"status": "ok"})
		},
	})

	client := NewRelayClient("dmz1", srv.URL, "tok")
	err := client.Upload(context.Background(), "host-b", UploadRequest{
		Dest: "/tmp/file.txt",
		Data: "aGVsbG8=", // base64("hello")
		Mode: "0644",
	})
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if capturedReq.Dest != "/tmp/file.txt" {
		t.Errorf("dest not transmitted: %q", capturedReq.Dest)
	}
	if capturedReq.Data != "aGVsbG8=" {
		t.Errorf("data not transmitted: %q", capturedReq.Data)
	}
}

// ── Fetch ─────────────────────────────────────────────────────────────────────

func TestRelayClient_Fetch_Success(t *testing.T) {
	var capturedReq FetchRequest
	srv := mockRelay(t, map[string]http.HandlerFunc{
		"POST /api/fetch/host-c": func(w http.ResponseWriter, r *http.Request) {
			json.NewDecoder(r.Body).Decode(&capturedReq) //nolint:errcheck
			jsonResponse(w, 200, FetchResponse{Data: "ZmlsZWNvbnRlbnQ=", RC: 0})
		},
	})

	client := NewRelayClient("dmz1", srv.URL, "tok")
	resp, err := client.Fetch(context.Background(), "host-c", FetchRequest{Src: "/etc/hosts"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if resp.Data != "ZmlsZWNvbnRlbnQ=" {
		t.Errorf("unexpected data: %q", resp.Data)
	}
	if capturedReq.Src != "/etc/hosts" {
		t.Errorf("src not transmitted: %q", capturedReq.Src)
	}
}

// ── PushManager ───────────────────────────────────────────────────────────────

func TestPushManager_PollsInventoryAndUpdatesRouting(t *testing.T) {
	// Create an in-memory store
	store, err := storage.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	// Register a relay node in push mode (token_hash stores plain token)
	node := storage.RelayNode{
		ID:        "uuid-push-1",
		RelayID:   "push-dmz1",
		Mode:      "push",
		TokenHash: "mock-token",
		Status:    "pending",
		CreatedAt: time.Now().Unix(),
	}

	// Mock relay serving inventory
	srv := mockRelay(t, map[string]http.HandlerFunc{
		"GET /api/inventory": func(w http.ResponseWriter, r *http.Request) {
			jsonResponse(w, 200, map[string]interface{}{
				"all": map[string]interface{}{
					"hosts": []string{"push-host-1", "push-host-2"},
				},
				"_meta": map[string]interface{}{
					"hostvars": map[string]interface{}{
						"push-host-1": map[string]interface{}{"secagent_status": "connected"},
						"push-host-2": map[string]interface{}{"secagent_status": "connected"},
					},
				},
			})
		},
	})

	// Set relay URL to the mock server
	node.URL = srv.URL
	if err := store.UpsertRelayNode(node); err != nil {
		t.Fatalf("UpsertRelayNode: %v", err)
	}

	// Start PushManager with a very short poll interval
	mgr := NewPushManager(store, 200*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())

	go mgr.Start(ctx)
	defer cancel()

	// Wait for at least one poll to complete
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		relayID, _ := store.GetRelayForHostname("push-host-1")
		if relayID == "push-dmz1" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Verify routing was updated
	for _, h := range []string{"push-host-1", "push-host-2"} {
		rid, err := store.GetRelayForHostname(h)
		if err != nil {
			t.Fatalf("GetRelayForHostname %s: %v", h, err)
		}
		if rid != "push-dmz1" {
			t.Errorf("expected %s routed to push-dmz1, got %q", h, rid)
		}
	}
}

func TestPushManager_BackoffOnFailure(t *testing.T) {
	store, err := storage.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	// Register a relay that points to a non-existent server
	node := storage.RelayNode{
		ID:        "uuid-push-fail",
		RelayID:   "push-fail",
		Mode:      "push",
		TokenHash: "tok",
		URL:       "http://localhost:1", // unreachable
		Status:    "pending",
		CreatedAt: time.Now().Unix(),
	}
	_ = store.UpsertRelayNode(node)

	mgr := NewPushManager(store, 500*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	// Should not panic, should log and backoff
	go mgr.Start(ctx)
	<-ctx.Done()

	// Relay should be marked disconnected
	n, _ := store.GetRelayNode("push-fail")
	if n != nil && n.Status == "connected" {
		t.Error("relay should be disconnected after failed polls")
	}
}

func TestPushManager_ActiveRelayCount(t *testing.T) {
	store, err := storage.NewStore(":memory:")
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	mgr := NewPushManager(store, time.Hour)
	if n := mgr.ActiveRelayCount(); n != 0 {
		t.Errorf("expected 0 active relays, got %d", n)
	}
}

func TestNewRelayClient_Defaults(t *testing.T) {
	c := NewRelayClient("dmz1", "https://relay:7770", "tok")
	if c.RelayID != "dmz1" {
		t.Errorf("expected RelayID=dmz1, got %s", c.RelayID)
	}
	if c.BaseURL != "https://relay:7770" {
		t.Errorf("unexpected BaseURL: %s", c.BaseURL)
	}
	if c.httpClient == nil {
		t.Error("expected httpClient to be set")
	}
}

func TestNewRelayClient_StripsTrailingSlash(t *testing.T) {
	c := NewRelayClient("dmz1", "https://relay:7770/", "tok")
	if strings.HasSuffix(c.BaseURL, "/") {
		t.Errorf("expected trailing slash stripped, got %q", c.BaseURL)
	}
}
