package webhooks

// NOTE: Ce fichier fait partie du package "webhooks" (défini dans dispatcher.go).
// Il compile une fois que dispatcher.go (tâche 11.2) et store_webhooks.go (tâche 11.1)
// sont présents.
//
// Commande de test :
//   JWT_SECRET_KEY=test ADMIN_TOKEN=test go test ./internal/webhooks/... -v

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"secagent-server/cmd/secagent-server/internal/storage"
)

// ========================================================================
// Mock store
// ========================================================================

// mockWebhookStore implémente WebhookStorer en mémoire.
// Les webhooks sont configurés à la construction ; les deliveries sont capturés.
type mockWebhookStore struct {
	mu         sync.Mutex
	webhooks   []storage.WebhookRecord
	deliveries []storage.DeliveryRecord
	notify     chan struct{}
}

func newMockStore(whs ...storage.WebhookRecord) *mockWebhookStore {
	return &mockWebhookStore{
		webhooks: whs,
		notify:   make(chan struct{}, 256),
	}
}

func (m *mockWebhookStore) ListWebhooksByEvent(_ context.Context, event string) ([]storage.WebhookRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []storage.WebhookRecord
	for _, wh := range m.webhooks {
		for _, ev := range wh.Events {
			if ev == event {
				result = append(result, wh)
				break
			}
		}
	}
	return result, nil
}

func (m *mockWebhookStore) CreateDelivery(_ context.Context, rec storage.DeliveryRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deliveries = append(m.deliveries, rec)
	select {
	case m.notify <- struct{}{}:
	default:
	}
	return nil
}

// waitDeliveries bloque jusqu'à ce que n deliveries soient enregistrées,
// ou échoue le test si le timeout est dépassé.
func (m *mockWebhookStore) waitDeliveries(t *testing.T, n int, timeout time.Duration) []storage.DeliveryRecord {
	t.Helper()
	deadline := time.After(timeout)
	for {
		m.mu.Lock()
		count := len(m.deliveries)
		m.mu.Unlock()
		if count >= n {
			m.mu.Lock()
			defer m.mu.Unlock()
			return append([]storage.DeliveryRecord(nil), m.deliveries...)
		}
		select {
		case <-m.notify:
			// une delivery créée, on re-vérifie
		case <-deadline:
			m.mu.Lock()
			got := len(m.deliveries)
			m.mu.Unlock()
			t.Fatalf("timeout waiting for %d deliveries, got %d", n, got)
			return nil
		}
	}
}

// deliveryCount retourne le nombre de deliveries capturées (thread-safe).
func (m *mockWebhookStore) deliveryCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.deliveries)
}

// ========================================================================
// Helper : démarrer un Dispatcher avec context annulé à la fin du test
// ========================================================================

func startDispatcher(t *testing.T, store WebhookStorer, queueSize int) *Dispatcher {
	t.Helper()
	d := NewDispatcher(store, queueSize)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d.Start(ctx)
	return d
}

// ========================================================================
// Helper : webhook record minimal pour les tests
// ========================================================================

func testWebhookRecord(id, url string, events []string) storage.WebhookRecord {
	return storage.WebhookRecord{
		ID:             id,
		URL:            url,
		Events:         events,
		Secret:         "",
		MaxRetries:     0, // pas de retry par défaut dans les tests
		TimeoutSeconds: 5,
		Enabled:        true,
		CreatedAt:      time.Now().UTC(),
	}
}

// ========================================================================
// TestDispatchSuccess
// Webhook configuré + dispatch → POST reçu avec bon payload JSON.
// ========================================================================

func TestDispatchSuccess(t *testing.T) {
	var receivedBody []byte
	var receivedOnce sync.Once
	done := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedOnce.Do(func() {
			receivedBody = body
			close(done)
		})
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	store := newMockStore(testWebhookRecord("wh-success", srv.URL, []string{string(EventHostNew)}))
	d := startDispatcher(t, store, 10)

	payload := EventPayload{
		Event:     string(EventHostNew),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Host: HostInfo{
			Hostname:   "srv-01",
			Status:     "disconnected",
			EnrolledAt: time.Now().UTC().Format(time.RFC3339),
		},
	}
	d.Dispatch(EventHostNew, payload)

	// Attendre la delivery côté serveur HTTP
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: webhook POST not received within 3s")
	}

	// Vérifier que le corps est du JSON valide avec les bons champs
	var body map[string]interface{}
	if err := json.Unmarshal(receivedBody, &body); err != nil {
		t.Fatalf("invalid JSON body: %v — raw: %s", err, receivedBody)
	}
	if body["event"] != string(EventHostNew) {
		t.Errorf("event: got %v, want %q", body["event"], string(EventHostNew))
	}
	if body["timestamp"] == "" {
		t.Error("expected non-empty timestamp")
	}

	// Attendre la delivery DB
	deliveries := store.waitDeliveries(t, 1, 3*time.Second)
	if !deliveries[0].Success {
		t.Errorf("expected delivery success=true, got false (statusCode=%v err=%q)",
			deliveries[0].StatusCode, deliveries[0].Error)
	}
	if deliveries[0].StatusCode == nil || *deliveries[0].StatusCode != 200 {
		t.Errorf("expected status_code=200, got %v", deliveries[0].StatusCode)
	}
}

// ========================================================================
// TestDispatchRetryOnFailure
// Serveur renvoie 500 → N+1 tentatives dans les deliveries (avec max_retries=1).
// ========================================================================

func TestDispatchRetryOnFailure(t *testing.T) {
	var callCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusInternalServerError) // 500
	}))
	defer srv.Close()

	wh := testWebhookRecord("wh-retry", srv.URL, []string{string(EventHostUp)})
	wh.MaxRetries = 1       // 1 retry → 2 tentatives au total
	wh.TimeoutSeconds = 2

	store := newMockStore(wh)
	d := startDispatcher(t, store, 10)

	payload := EventPayload{
		Event:     string(EventHostUp),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Host:      HostInfo{Hostname: "srv-01", Status: "connected"},
	}
	d.Dispatch(EventHostUp, payload)

	// MaxRetries=1 → backoff = 1s avant 2ème tentative.
	// On attend 2 deliveries en DB avec un timeout généreux.
	deliveries := store.waitDeliveries(t, 2, 10*time.Second)

	if len(deliveries) != 2 {
		t.Errorf("expected 2 deliveries (1 initial + 1 retry), got %d", len(deliveries))
	}
	for i, d := range deliveries {
		if d.Success {
			t.Errorf("delivery[%d]: expected success=false (server returned 500)", i)
		}
		if d.StatusCode == nil || *d.StatusCode != 500 {
			t.Errorf("delivery[%d]: expected status_code=500, got %v", i, d.StatusCode)
		}
	}
	// Les numéros de tentatives doivent être 1 et 2
	attempts := make(map[int]bool)
	for _, d := range deliveries {
		attempts[d.Attempt] = true
	}
	if !attempts[1] || !attempts[2] {
		t.Errorf("expected attempt=1 and attempt=2, got attempts=%v", attempts)
	}
}

// ========================================================================
// TestDispatch4xxNoRetry
// Serveur renvoie 404 → 1 seule tentative (4xx = pas de retry).
// ========================================================================

func TestDispatch4xxNoRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound) // 404
	}))
	defer srv.Close()

	wh := testWebhookRecord("wh-4xx", srv.URL, []string{string(EventHostDown)})
	wh.MaxRetries = 3 // retry autorisé, mais 4xx ne doit PAS retenter

	store := newMockStore(wh)
	d := startDispatcher(t, store, 10)

	payload := EventPayload{
		Event:     string(EventHostDown),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Host:      HostInfo{Hostname: "srv-01", Status: "disconnected"},
	}
	d.Dispatch(EventHostDown, payload)

	// Attendre la 1ère delivery
	store.waitDeliveries(t, 1, 3*time.Second)

	// Attendre un peu puis vérifier qu'il n'y a PAS eu de retry
	time.Sleep(200 * time.Millisecond)
	count := store.deliveryCount()
	if count != 1 {
		t.Errorf("expected exactly 1 delivery (no retry on 4xx), got %d", count)
	}

	// Vérifier que la delivery est marquée failure avec le bon status code
	store.mu.Lock()
	d0 := store.deliveries[0]
	store.mu.Unlock()
	if d0.Success {
		t.Error("expected success=false for 404 response")
	}
	if d0.StatusCode == nil || *d0.StatusCode != 404 {
		t.Errorf("expected status_code=404, got %v", d0.StatusCode)
	}
}

// ========================================================================
// TestDispatchHMAC
// Webhook avec secret → header X-Signature présent et valide.
// ========================================================================

func TestDispatchHMAC(t *testing.T) {
	const secret = "test-hmac-secret"

	var (
		capturedSig  string
		capturedBody []byte
		sigMu        sync.Mutex
		sigDone      = make(chan struct{})
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get("X-Signature")
		sigMu.Lock()
		capturedBody = body
		capturedSig = sig
		sigMu.Unlock()
		select {
		case sigDone <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := testWebhookRecord("wh-hmac", srv.URL, []string{string(EventHostNew)})
	wh.Secret = secret

	store := newMockStore(wh)
	d := startDispatcher(t, store, 10)

	payload := EventPayload{
		Event:     string(EventHostNew),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Host:      HostInfo{Hostname: "srv-hmac", Status: "disconnected"},
	}
	d.Dispatch(EventHostNew, payload)

	select {
	case <-sigDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: no request received by httptest server")
	}

	sigMu.Lock()
	sig := capturedSig
	body := capturedBody
	sigMu.Unlock()

	if sig == "" {
		t.Fatal("expected X-Signature header to be present when secret is set")
	}

	// Vérifier la signature : sha256=hex(HMAC-SHA256(secret, body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if sig != expected {
		t.Errorf("X-Signature mismatch:\n  got      %q\n  expected %q", sig, expected)
	}
}

// ========================================================================
// TestDispatchNoSecret
// Webhook sans secret → pas de header X-Signature.
// ========================================================================

func TestDispatchNoSecret(t *testing.T) {
	var headerDone = make(chan string, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case headerDone <- r.Header.Get("X-Signature"):
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wh := testWebhookRecord("wh-no-secret", srv.URL, []string{string(EventHostUp)})
	wh.Secret = "" // pas de secret

	store := newMockStore(wh)
	d := startDispatcher(t, store, 10)

	payload := EventPayload{
		Event:     string(EventHostUp),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Host:      HostInfo{Hostname: "srv-01", Status: "connected"},
	}
	d.Dispatch(EventHostUp, payload)

	select {
	case sig := <-headerDone:
		if sig != "" {
			t.Errorf("expected no X-Signature header when secret is empty, got %q", sig)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: no request received")
	}
}

// ========================================================================
// TestDispatchQueueFull
// Queue de taille 1, 100 events envoyés rapidement → pas de deadlock, drops loggués.
// ========================================================================

func TestDispatchQueueFull(t *testing.T) {
	// Store sans webhooks → traitement immédiat (ListWebhooksByEvent retourne vide).
	// Cela garantit que le worker libère la queue rapidement,
	// validant que Dispatch() ne bloque jamais même si la queue déborde momentanément.
	store := newMockStore() // aucun webhook
	d := startDispatcher(t, store, 1)

	payload := EventPayload{
		Event:     string(EventHostUp),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Host:      HostInfo{Hostname: "load-test", Status: "connected"},
	}

	// Envoyer 100 events dans une goroutine — doit se terminer sans deadlock
	dispatched := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			d.Dispatch(EventHostUp, payload)
		}
		close(dispatched)
	}()

	select {
	case <-dispatched:
		// Succès — les 100 appels Dispatch() sont retournés sans bloquer
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: 100 Dispatch() calls did not return within 2s with queue size 1")
	}
}

// ========================================================================
// TestDispatchHeaders
// Vérifier Content-Type, X-Event, X-Webhook-ID, User-Agent sur la requête sortante.
// ========================================================================

func TestDispatchHeaders(t *testing.T) {
	type capturedHeaders struct {
		contentType string
		xEvent      string
		webhookID   string
		userAgent   string
	}

	headerCh := make(chan capturedHeaders, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case headerCh <- capturedHeaders{
			contentType: r.Header.Get("Content-Type"),
			xEvent:      r.Header.Get("X-Event"),
			webhookID:   r.Header.Get("X-Webhook-ID"),
			userAgent:   r.Header.Get("User-Agent"),
		}:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	const whID = "wh-headers"
	wh := testWebhookRecord(whID, srv.URL, []string{string(EventHostNew)})

	store := newMockStore(wh)
	d := startDispatcher(t, store, 10)

	payload := EventPayload{
		Event:     string(EventHostNew),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Host:      HostInfo{Hostname: "srv-header", Status: "disconnected"},
	}
	d.Dispatch(EventHostNew, payload)

	select {
	case h := <-headerCh:
		if h.contentType != "application/json" {
			t.Errorf("Content-Type: got %q, want %q", h.contentType, "application/json")
		}
		if h.xEvent != string(EventHostNew) {
			t.Errorf("X-Event: got %q, want %q", h.xEvent, string(EventHostNew))
		}
		if h.webhookID != whID {
			t.Errorf("X-Webhook-ID: got %q, want %q", h.webhookID, whID)
		}
		if h.userAgent != "secagent-server/1.0" {
			t.Errorf("User-Agent: got %q, want %q", h.userAgent, "secagent-server/1.0")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout: no request received by httptest server")
	}
}

// ========================================================================
// TestDispatchNoWebhookForEvent
// Aucun webhook enregistré pour l'event → aucune delivery, aucune erreur.
// ========================================================================

func TestDispatchNoWebhookForEvent(t *testing.T) {
	// Webhook configuré pour host.down, on dispatch host.new → aucun match
	wh := testWebhookRecord("wh-nomatch", "https://example.com/hook", []string{string(EventHostDown)})
	store := newMockStore(wh)
	d := startDispatcher(t, store, 10)

	payload := EventPayload{
		Event:     string(EventHostNew),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Host:      HostInfo{Hostname: "srv-01", Status: "disconnected"},
	}
	d.Dispatch(EventHostNew, payload)

	// Attendre un peu et vérifier qu'aucune delivery n'a été créée
	time.Sleep(100 * time.Millisecond)
	if count := store.deliveryCount(); count != 0 {
		t.Errorf("expected 0 deliveries for non-matching event, got %d", count)
	}
}
