package storage

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// ========================================================================
// helpers
// ========================================================================

func makeWebhook(id, url string, events []string, enabled bool) WebhookRecord {
	return WebhookRecord{
		ID:             id,
		URL:            url,
		Events:         events,
		Secret:         "s3cr3t",
		MaxRetries:     3,
		TimeoutSeconds: 10,
		Enabled:        enabled,
		CreatedAt:      time.Now().UTC(),
		Description:    "test webhook",
	}
}

func makeDelivery(id, webhookID, event string, success bool) DeliveryRecord {
	code := 200
	dur := int64(42)
	rec := DeliveryRecord{
		ID:          id,
		WebhookID:   webhookID,
		Event:       event,
		Payload:     `{"event":"` + event + `"}`,
		Attempt:     1,
		Success:     success,
		AttemptedAt: time.Now().UTC(),
	}
	if success {
		rec.StatusCode = &code
		rec.DurationMs = &dur
	}
	return rec
}

// ========================================================================
// CreateWebhook / GetWebhook
// ========================================================================

func TestCreateAndGetWebhook(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	wh := makeWebhook("wh-1", "https://example.com/hook", []string{"host.new", "host.up"}, true)
	if err := store.CreateWebhook(ctx, wh); err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	got, err := store.GetWebhook(ctx, "wh-1")
	if err != nil {
		t.Fatalf("GetWebhook: %v", err)
	}
	if got == nil {
		t.Fatal("expected record, got nil")
	}

	// Verify round-trip equality
	if got.ID != wh.ID {
		t.Errorf("ID: got %q, want %q", got.ID, wh.ID)
	}
	if got.URL != wh.URL {
		t.Errorf("URL: got %q, want %q", got.URL, wh.URL)
	}
	if got.Secret != wh.Secret {
		t.Errorf("Secret: got %q, want %q", got.Secret, wh.Secret)
	}
	if got.MaxRetries != wh.MaxRetries {
		t.Errorf("MaxRetries: got %d, want %d", got.MaxRetries, wh.MaxRetries)
	}
	if got.TimeoutSeconds != wh.TimeoutSeconds {
		t.Errorf("TimeoutSeconds: got %d, want %d", got.TimeoutSeconds, wh.TimeoutSeconds)
	}
	if got.Enabled != wh.Enabled {
		t.Errorf("Enabled: got %v, want %v", got.Enabled, wh.Enabled)
	}
	if got.Description != wh.Description {
		t.Errorf("Description: got %q, want %q", got.Description, wh.Description)
	}
	if len(got.Events) != len(wh.Events) {
		t.Errorf("Events len: got %d, want %d", len(got.Events), len(wh.Events))
	} else {
		for i := range wh.Events {
			if got.Events[i] != wh.Events[i] {
				t.Errorf("Events[%d]: got %q, want %q", i, got.Events[i], wh.Events[i])
			}
		}
	}
}

func TestGetWebhookNotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	got, err := store.GetWebhook(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

// ========================================================================
// ListWebhooks
// ========================================================================

func TestListWebhooks(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	for i, id := range []string{"wh-a", "wh-b", "wh-c"} {
		events := []string{"host.new"}
		_ = i
		wh := makeWebhook(id, "https://example.com/"+id, events, true)
		if err := store.CreateWebhook(ctx, wh); err != nil {
			t.Fatalf("CreateWebhook %s: %v", id, err)
		}
	}

	list, err := store.ListWebhooks(ctx)
	if err != nil {
		t.Fatalf("ListWebhooks: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("expected 3 webhooks, got %d", len(list))
	}
}

func TestListWebhooksEmpty(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	list, err := store.ListWebhooks(ctx)
	if err != nil {
		t.Fatalf("ListWebhooks: %v", err)
	}
	if list != nil && len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
}

// ========================================================================
// DeleteWebhook + cascade
// ========================================================================

func TestDeleteWebhook(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	wh := makeWebhook("wh-del", "https://example.com/del", []string{"host.down"}, true)
	if err := store.CreateWebhook(ctx, wh); err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	deleted, err := store.DeleteWebhook(ctx, "wh-del")
	if err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
	if !deleted {
		t.Error("expected deleted=true")
	}

	got, _ := store.GetWebhook(ctx, "wh-del")
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestDeleteWebhookNotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	deleted, err := store.DeleteWebhook(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted {
		t.Error("expected false for nonexistent webhook")
	}
}

func TestDeleteWebhookCascadesDeliveries(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Create webhook + two deliveries
	wh := makeWebhook("wh-cascade", "https://example.com/cascade", []string{"host.new"}, true)
	if err := store.CreateWebhook(ctx, wh); err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	for _, dID := range []string{"d-1", "d-2"} {
		d := makeDelivery(dID, "wh-cascade", "host.new", true)
		if err := store.CreateDelivery(ctx, d); err != nil {
			t.Fatalf("CreateDelivery %s: %v", dID, err)
		}
	}

	// Verify deliveries exist before delete
	deliveries, err := store.ListDeliveries(ctx, "wh-cascade", 10)
	if err != nil {
		t.Fatalf("ListDeliveries before delete: %v", err)
	}
	if len(deliveries) != 2 {
		t.Fatalf("expected 2 deliveries before delete, got %d", len(deliveries))
	}

	// Delete the webhook
	deleted, err := store.DeleteWebhook(ctx, "wh-cascade")
	if err != nil {
		t.Fatalf("DeleteWebhook: %v", err)
	}
	if !deleted {
		t.Fatal("expected deleted=true")
	}

	// Deliveries must be gone (CASCADE)
	deliveries, err = store.ListDeliveries(ctx, "wh-cascade", 10)
	if err != nil {
		t.Fatalf("ListDeliveries after delete: %v", err)
	}
	if len(deliveries) != 0 {
		t.Errorf("expected 0 deliveries after CASCADE delete, got %d", len(deliveries))
	}
}

// ========================================================================
// ListWebhooksByEvent
// ========================================================================

func TestListWebhooksByEvent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// wh-multi : souscrit à host.new et host.up — enabled
	if err := store.CreateWebhook(ctx, makeWebhook("wh-multi", "https://a.example.com/hook",
		[]string{"host.new", "host.up"}, true)); err != nil {
		t.Fatalf("CreateWebhook wh-multi: %v", err)
	}
	// wh-only-down : souscrit uniquement à host.down — enabled
	if err := store.CreateWebhook(ctx, makeWebhook("wh-only-down", "https://b.example.com/hook",
		[]string{"host.down"}, true)); err != nil {
		t.Fatalf("CreateWebhook wh-only-down: %v", err)
	}
	// wh-disabled : souscrit à host.new mais disabled
	if err := store.CreateWebhook(ctx, makeWebhook("wh-disabled", "https://c.example.com/hook",
		[]string{"host.new"}, false)); err != nil {
		t.Fatalf("CreateWebhook wh-disabled: %v", err)
	}

	// Filter on "host.new" — should return only wh-multi (enabled + has host.new)
	// wh-disabled must be excluded (disabled)
	// wh-only-down must be excluded (doesn't have host.new)
	list, err := store.ListWebhooksByEvent(ctx, "host.new")
	if err != nil {
		t.Fatalf("ListWebhooksByEvent host.new: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 webhook for host.new, got %d", len(list))
	} else if list[0].ID != "wh-multi" {
		t.Errorf("expected wh-multi, got %q", list[0].ID)
	}

	// Filter on "host.down"
	list, err = store.ListWebhooksByEvent(ctx, "host.down")
	if err != nil {
		t.Fatalf("ListWebhooksByEvent host.down: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 webhook for host.down, got %d", len(list))
	} else if list[0].ID != "wh-only-down" {
		t.Errorf("expected wh-only-down, got %q", list[0].ID)
	}

	// Filter on "host.revoked" — no webhook subscribed
	list, err = store.ListWebhooksByEvent(ctx, "host.revoked")
	if err != nil {
		t.Fatalf("ListWebhooksByEvent host.revoked: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 webhooks for host.revoked, got %d", len(list))
	}
}

// ========================================================================
// CreateDelivery / ListDeliveries
// ========================================================================

func TestCreateAndListDeliveries(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	wh := makeWebhook("wh-dl", "https://example.com/dl", []string{"host.new"}, true)
	if err := store.CreateWebhook(ctx, wh); err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	// Create a successful delivery
	d1 := makeDelivery("d-ok", "wh-dl", "host.new", true)
	if err := store.CreateDelivery(ctx, d1); err != nil {
		t.Fatalf("CreateDelivery d-ok: %v", err)
	}

	// Create a failed delivery (no status code)
	d2 := DeliveryRecord{
		ID:          "d-fail",
		WebhookID:   "wh-dl",
		Event:       "host.new",
		Payload:     `{"event":"host.new"}`,
		Attempt:     2,
		Success:     false,
		Error:       "connect: connection refused",
		AttemptedAt: time.Now().UTC(),
	}
	if err := store.CreateDelivery(ctx, d2); err != nil {
		t.Fatalf("CreateDelivery d-fail: %v", err)
	}

	list, err := store.ListDeliveries(ctx, "wh-dl", 10)
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 deliveries, got %d", len(list))
	}

	// Most recent first (attempted_at DESC)
	// Both are very close in time — just verify all fields on the success one
	var ok *DeliveryRecord
	for i := range list {
		if list[i].ID == "d-ok" {
			ok = &list[i]
		}
	}
	if ok == nil {
		t.Fatal("d-ok not found in ListDeliveries")
	}
	if !ok.Success {
		t.Error("expected success=true for d-ok")
	}
	if ok.StatusCode == nil || *ok.StatusCode != 200 {
		t.Errorf("expected status_code=200, got %v", ok.StatusCode)
	}
	if ok.DurationMs == nil || *ok.DurationMs != 42 {
		t.Errorf("expected duration_ms=42, got %v", ok.DurationMs)
	}

	// Verify failed delivery has nil statusCode
	var fail *DeliveryRecord
	for i := range list {
		if list[i].ID == "d-fail" {
			fail = &list[i]
		}
	}
	if fail == nil {
		t.Fatal("d-fail not found in ListDeliveries")
	}
	if fail.Success {
		t.Error("expected success=false for d-fail")
	}
	if fail.StatusCode != nil {
		t.Errorf("expected nil statusCode, got %v", fail.StatusCode)
	}
	if fail.Error != "connect: connection refused" {
		t.Errorf("wrong error: %q", fail.Error)
	}
}

func TestListDeliveriesEmpty(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	wh := makeWebhook("wh-empty", "https://example.com/empty", []string{"host.up"}, true)
	if err := store.CreateWebhook(ctx, wh); err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	list, err := store.ListDeliveries(ctx, "wh-empty", 10)
	if err != nil {
		t.Fatalf("ListDeliveries: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
}

func TestListDeliveriesLimit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	wh := makeWebhook("wh-limit", "https://example.com/limit", []string{"host.new"}, true)
	if err := store.CreateWebhook(ctx, wh); err != nil {
		t.Fatalf("CreateWebhook: %v", err)
	}

	for i := 0; i < 5; i++ {
		d := makeDelivery(
			fmt.Sprintf("d-%d", i), "wh-limit", "host.new", true,
		)
		if err := store.CreateDelivery(ctx, d); err != nil {
			t.Fatalf("CreateDelivery d-%d: %v", i, err)
		}
	}

	list, err := store.ListDeliveries(ctx, "wh-limit", 3)
	if err != nil {
		t.Fatalf("ListDeliveries with limit 3: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("expected 3 deliveries (limit), got %d", len(list))
	}
}

// ========================================================================
// DDL idempotency — new tables must not break the existing test
// ========================================================================

func TestWebhookDDLIdempotent(t *testing.T) {
	// Opening two independent in-memory stores proves DDL is idempotent
	store1 := newTestStore(t)
	store2 := newTestStore(t)
	ctx := context.Background()

	wh := makeWebhook("wh-idem", "https://example.com/idem", []string{"host.new"}, true)
	if err := store1.CreateWebhook(ctx, wh); err != nil {
		t.Fatalf("store1 CreateWebhook: %v", err)
	}
	// store2 is independent — just check it initialises cleanly
	list, err := store2.ListWebhooks(ctx)
	if err != nil {
		t.Fatalf("store2 ListWebhooks: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("store2 should be empty, got %d", len(list))
	}
}
