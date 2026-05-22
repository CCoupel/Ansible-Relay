package handlers

// Tests des handlers REST Phase 11 — Webhooks & Events
//
// Référence contrats : _work/contracts/phase11-webhooks-api.md
// Référence plan     : _work/plan/phase11-webhooks.md
//
// Ce fichier compile une fois que les implémentations suivantes sont présentes :
//   - internal/handlers/admin_webhooks.go   (tâche 11.3)
//   - internal/storage/store_webhooks.go    (tâche 11.1)
//   - internal/webhooks/dispatcher.go       (tâche 11.2)
//
// Note : webhooks.GlobalDispatcher est nil pendant les tests.
// L'implémentation de AdminDeleteMinion DOIT vérifier `if webhooks.GlobalDispatcher != nil`
// avant tout appel Dispatch pour éviter un nil-pointer panic.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"secagent-server/cmd/secagent-server/internal/storage"
)

// ========================================================================
// Helpers spécifiques aux webhooks
// ========================================================================

// seedWebhook insère un webhook directement dans adminStore et retourne son ID.
func seedWebhook(t *testing.T, s *storage.Store, id, url string, events []string) string {
	t.Helper()
	rec := storage.WebhookRecord{
		ID:             id,
		URL:            url,
		Events:         events,
		Secret:         "",
		MaxRetries:     3,
		TimeoutSeconds: 10,
		Enabled:        true,
		CreatedAt:      time.Now().UTC(),
		Description:    "seeded for test",
	}
	if err := s.CreateWebhook(context.Background(), rec); err != nil {
		t.Fatalf("seedWebhook %q: %v", id, err)
	}
	return id
}

// decodeJSON unmarshals the response body into v.
func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	if err := json.NewDecoder(w.Body).Decode(v); err != nil {
		t.Fatalf("decode JSON: %v — body: %s", err, w.Body.String())
	}
}

// ========================================================================
// POST /api/admin/webhooks
// ========================================================================

func TestAdminCreateWebhookOK(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)

	req := adminReq("POST", "/api/admin/webhooks", map[string]interface{}{
		"url":             "https://monitoring.internal/hook",
		"events":          []string{"host.new", "host.up"},
		"secret":          "s3cr3t",
		"max_retries":     2,
		"timeout_seconds": 15,
		"description":     "Test webhook",
	})
	w := httptest.NewRecorder()
	AdminCreateWebhook(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d — %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	decodeJSON(t, w, &resp)

	if resp["id"] == "" || resp["id"] == nil {
		t.Error("expected non-empty id in response")
	}
	if resp["url"] != "https://monitoring.internal/hook" {
		t.Errorf("url: got %v", resp["url"])
	}
	// Le secret ne doit PAS être retourné (toujours "" dans la réponse)
	if resp["secret"] != "" && resp["secret"] != nil {
		t.Errorf("secret should be empty in response (not exposed), got %v", resp["secret"])
	}
	if resp["enabled"] != true {
		t.Errorf("expected enabled=true by default, got %v", resp["enabled"])
	}
	events, _ := resp["events"].([]interface{})
	if len(events) != 2 {
		t.Errorf("expected 2 events, got %v", events)
	}
}

func TestAdminCreateWebhookInvalidURL(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)

	req := adminReq("POST", "/api/admin/webhooks", map[string]interface{}{
		"url":    "ftp://invalid-scheme.com/hook", // ni http:// ni https://
		"events": []string{"host.new"},
	})
	w := httptest.NewRecorder()
	AdminCreateWebhook(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid URL, got %d — %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	decodeJSON(t, w, &resp)
	if resp["error"] != "invalid_url" {
		t.Errorf("expected error=invalid_url, got %v", resp["error"])
	}
}

func TestAdminCreateWebhookMissingURL(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)

	req := adminReq("POST", "/api/admin/webhooks", map[string]interface{}{
		"events": []string{"host.new"},
		// url absent
	})
	w := httptest.NewRecorder()
	AdminCreateWebhook(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing URL, got %d — %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	decodeJSON(t, w, &resp)
	if resp["error"] != "missing_url" {
		t.Errorf("expected error=missing_url, got %v", resp["error"])
	}
}

func TestAdminCreateWebhookUnknownEvent(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)

	req := adminReq("POST", "/api/admin/webhooks", map[string]interface{}{
		"url":    "https://example.com/hook",
		"events": []string{"host.new", "host.xxx"}, // event inconnu
	})
	w := httptest.NewRecorder()
	AdminCreateWebhook(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown event, got %d — %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	decodeJSON(t, w, &resp)
	if resp["error"] != "invalid_event" {
		t.Errorf("expected error=invalid_event, got %v", resp["error"])
	}
	if resp["event"] != "host.xxx" {
		t.Errorf("expected event=host.xxx in response, got %v", resp["event"])
	}
}

func TestAdminCreateWebhookMissingEvents(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)

	req := adminReq("POST", "/api/admin/webhooks", map[string]interface{}{
		"url":    "https://example.com/hook",
		"events": []string{}, // vide
	})
	w := httptest.NewRecorder()
	AdminCreateWebhook(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty events, got %d — %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	decodeJSON(t, w, &resp)
	if resp["error"] != "missing_events" {
		t.Errorf("expected error=missing_events, got %v", resp["error"])
	}
}

func TestAdminCreateWebhookUnauthorized(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)

	req := httptest.NewRequest("POST", "/api/admin/webhooks", nil)
	// pas d'Authorization header
	w := httptest.NewRecorder()
	AdminCreateWebhook(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// ========================================================================
// GET /api/admin/webhooks
// ========================================================================

func TestAdminListWebhooks(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)

	// GET sur store vide → tableau vide (pas null)
	req := adminReq("GET", "/api/admin/webhooks", nil)
	w := httptest.NewRecorder()
	AdminListWebhooks(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — %s", w.Code, w.Body.String())
	}

	var result []interface{}
	decodeJSON(t, w, &result)
	if result == nil {
		t.Error("expected empty array [], got nil")
	}
	if len(result) != 0 {
		t.Errorf("expected 0 webhooks on empty store, got %d", len(result))
	}

	// Ajouter 2 webhooks et re-lister
	seedWebhook(t, s, "wh-l-1", "https://a.com/hook", []string{"host.new"})
	seedWebhook(t, s, "wh-l-2", "https://b.com/hook", []string{"host.up", "host.down"})

	req2 := adminReq("GET", "/api/admin/webhooks", nil)
	w2 := httptest.NewRecorder()
	AdminListWebhooks(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w2.Code)
	}

	var result2 []interface{}
	decodeJSON(t, w2, &result2)
	if len(result2) != 2 {
		t.Errorf("expected 2 webhooks, got %d", len(result2))
	}
}

// ========================================================================
// GET /api/admin/webhooks/{id}
// ========================================================================

func TestAdminGetWebhookOK(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)
	seedWebhook(t, s, "wh-get-1", "https://get.example.com/hook", []string{"host.new"})

	req := adminReq("GET", "/api/admin/webhooks/wh-get-1", nil)
	req.SetPathValue("id", "wh-get-1")
	w := httptest.NewRecorder()
	AdminGetWebhook(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	decodeJSON(t, w, &resp)
	if resp["id"] != "wh-get-1" {
		t.Errorf("id: got %v, want wh-get-1", resp["id"])
	}
	if resp["url"] != "https://get.example.com/hook" {
		t.Errorf("url: got %v", resp["url"])
	}
}

func TestAdminGetWebhookNotFound(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)

	req := adminReq("GET", "/api/admin/webhooks/unknown", nil)
	req.SetPathValue("id", "unknown")
	w := httptest.NewRecorder()
	AdminGetWebhook(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d — %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	decodeJSON(t, w, &resp)
	if resp["error"] != "webhook_not_found" {
		t.Errorf("expected error=webhook_not_found, got %v", resp["error"])
	}
}

// ========================================================================
// DELETE /api/admin/webhooks/{id}
// ========================================================================

func TestAdminDeleteWebhookOK(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)
	seedWebhook(t, s, "wh-del-1", "https://del.example.com/hook", []string{"host.down"})

	req := adminReq("DELETE", "/api/admin/webhooks/wh-del-1", nil)
	req.SetPathValue("id", "wh-del-1")
	w := httptest.NewRecorder()
	AdminDeleteWebhook(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	decodeJSON(t, w, &resp)
	if resp["id"] != "wh-del-1" {
		t.Errorf("id: got %v", resp["id"])
	}
	if resp["status"] != "deleted" {
		t.Errorf("status: got %v, want deleted", resp["status"])
	}

	// Vérifier que le webhook est bien supprimé en DB
	got, err := s.GetWebhook(context.Background(), "wh-del-1")
	if err != nil {
		t.Fatalf("GetWebhook after delete: %v", err)
	}
	if got != nil {
		t.Error("expected webhook to be gone from DB after deletion")
	}
}

func TestAdminDeleteWebhookNotFound(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)

	req := adminReq("DELETE", "/api/admin/webhooks/nonexistent", nil)
	req.SetPathValue("id", "nonexistent")
	w := httptest.NewRecorder()
	AdminDeleteWebhook(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d — %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	decodeJSON(t, w, &resp)
	if resp["error"] != "webhook_not_found" {
		t.Errorf("expected error=webhook_not_found, got %v", resp["error"])
	}
}

// ========================================================================
// GET /api/admin/webhooks/{id}/deliveries
// ========================================================================

func TestAdminListDeliveries(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)
	seedWebhook(t, s, "wh-dlv-1", "https://dlv.example.com/hook", []string{"host.new"})

	// Sans delivery → tableau vide
	req := adminReq("GET", "/api/admin/webhooks/wh-dlv-1/deliveries", nil)
	req.SetPathValue("id", "wh-dlv-1")
	w := httptest.NewRecorder()
	AdminListDeliveries(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — %s", w.Code, w.Body.String())
	}

	var result []interface{}
	decodeJSON(t, w, &result)
	if result == nil {
		t.Error("expected [] not null for empty deliveries")
	}
}

func TestAdminListDeliveriesInvalidLimit(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)
	seedWebhook(t, s, "wh-limit-1", "https://limit.example.com/hook", []string{"host.new"})

	req := adminReq("GET", "/api/admin/webhooks/wh-limit-1/deliveries?limit=999", nil)
	req.SetPathValue("id", "wh-limit-1")
	w := httptest.NewRecorder()
	AdminListDeliveries(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for limit=999 (>200), got %d — %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	decodeJSON(t, w, &resp)
	if resp["error"] != "invalid_limit" {
		t.Errorf("expected error=invalid_limit, got %v", resp["error"])
	}
}

func TestAdminListDeliveriesLimitZero(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)
	seedWebhook(t, s, "wh-limz", "https://limz.example.com/hook", []string{"host.new"})

	req := adminReq("GET", "/api/admin/webhooks/wh-limz/deliveries?limit=0", nil)
	req.SetPathValue("id", "wh-limz")
	w := httptest.NewRecorder()
	AdminListDeliveries(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for limit=0 (<1), got %d — %s", w.Code, w.Body.String())
	}
}

func TestAdminListDeliveriesWebhookNotFound(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)

	req := adminReq("GET", "/api/admin/webhooks/ghost-id/deliveries", nil)
	req.SetPathValue("id", "ghost-id")
	w := httptest.NewRecorder()
	AdminListDeliveries(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown webhook, got %d — %s", w.Code, w.Body.String())
	}
}

// ========================================================================
// DELETE /api/admin/minions/{hostname}  — AdminDeleteMinion
//
// Note : si webhooks.GlobalDispatcher est non-nil en test, l'événement
// host.deleted est dispatché de façon asynchrone. L'implémentation DOIT
// vérifier `if webhooks.GlobalDispatcher != nil` pour ne pas paniquer.
// ========================================================================

func TestAdminDeleteMinionOK(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)
	seedAgent(t, s, "host-to-delete")

	req := adminReq("DELETE", "/api/admin/minions/host-to-delete", nil)
	req.SetPathValue("hostname", "host-to-delete")
	w := httptest.NewRecorder()
	AdminDeleteMinion(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d — %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	decodeJSON(t, w, &resp)
	if resp["hostname"] != "host-to-delete" {
		t.Errorf("hostname: got %v", resp["hostname"])
	}
	if resp["status"] != "deleted" {
		t.Errorf("status: got %v, want deleted", resp["status"])
	}
	// ws_disconnected est un bool (false = pas de WS active, ce qui est normal en test)
	if _, ok := resp["ws_disconnected"]; !ok {
		t.Error("expected ws_disconnected field in response")
	}

	// L'agent doit avoir disparu de la DB
	got, err := s.GetAgent(context.Background(), "host-to-delete")
	if err != nil {
		t.Fatalf("GetAgent after delete: %v", err)
	}
	if got != nil {
		t.Error("expected agent to be deleted from DB")
	}
}

func TestAdminDeleteMinionNotFound(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)

	req := adminReq("DELETE", "/api/admin/minions/unknown-host", nil)
	req.SetPathValue("hostname", "unknown-host")
	w := httptest.NewRecorder()
	AdminDeleteMinion(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown hostname, got %d — %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	decodeJSON(t, w, &resp)
	if resp["error"] != "agent_not_found" {
		t.Errorf("expected error=agent_not_found, got %v", resp["error"])
	}
}

func TestAdminDeleteMinionUnauthorized(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)

	req := httptest.NewRequest("DELETE", "/api/admin/minions/some-host", nil)
	// pas de Authorization header
	w := httptest.NewRecorder()
	AdminDeleteMinion(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

// ========================================================================
// Intégration : créer via API puis lister/supprimer
// ========================================================================

func TestAdminWebhookFullLifecycle(t *testing.T) {
	s := newTestStore(t)
	SetAdminStore(s)

	// 1. Créer un webhook via API
	createReq := adminReq("POST", "/api/admin/webhooks", map[string]interface{}{
		"url":    "https://lifecycle.example.com/hook",
		"events": []string{"host.new", "host.revoked"},
	})
	createW := httptest.NewRecorder()
	AdminCreateWebhook(createW, createReq)
	if createW.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d", createW.Code)
	}

	var created map[string]interface{}
	decodeJSON(t, createW, &created)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("create: expected non-empty id")
	}

	// 2. Lister → doit contenir le webhook
	listReq := adminReq("GET", "/api/admin/webhooks", nil)
	listW := httptest.NewRecorder()
	AdminListWebhooks(listW, listReq)
	if listW.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", listW.Code)
	}
	var list []interface{}
	decodeJSON(t, listW, &list)
	if len(list) != 1 {
		t.Errorf("list: expected 1 webhook, got %d", len(list))
	}

	// 3. GET par ID
	getReq := adminReq("GET", "/api/admin/webhooks/"+id, nil)
	getReq.SetPathValue("id", id)
	getW := httptest.NewRecorder()
	AdminGetWebhook(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", getW.Code)
	}

	// 4. Supprimer
	delReq := adminReq("DELETE", "/api/admin/webhooks/"+id, nil)
	delReq.SetPathValue("id", id)
	delW := httptest.NewRecorder()
	AdminDeleteWebhook(delW, delReq)
	if delW.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d", delW.Code)
	}

	// 5. GET après suppression → 404
	getReq2 := adminReq("GET", "/api/admin/webhooks/"+id, nil)
	getReq2.SetPathValue("id", id)
	getW2 := httptest.NewRecorder()
	AdminGetWebhook(getW2, getReq2)
	if getW2.Code != http.StatusNotFound {
		t.Errorf("get after delete: expected 404, got %d", getW2.Code)
	}
}
