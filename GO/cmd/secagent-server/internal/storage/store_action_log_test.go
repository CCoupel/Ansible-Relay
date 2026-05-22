package storage

// Tests CreateActionLog / ListActionLogs — table action_log (hooks system).
//
// Référence : DOC/server/HOOKS_SPEC.md §7
//
// API réelle implémentée dans store_action_log.go :
//
//   ActionLogEntry {
//     ID, Event, Hostname, ActionType string
//     ActionIndex    int
//     ConfigSnapshot string   // JSON de l'ActionDef au moment de l'exécution
//     Success        bool
//     Error          string
//     DurationMs     int64
//     ExecutedAt     time.Time
//   }
//
//   ActionLogFilter { Event, Hostname string; Limit int }
//
//   (s *Store) CreateActionLog(ctx context.Context, entry ActionLogEntry) error
//   (s *Store) ListActionLogs(ctx context.Context, f ActionLogFilter) ([]ActionLogEntry, error)
//
// Filtres ActionLogFilter :
//   Event    = "" → tous les événements
//   Hostname = "" → tous les hostnames
//   Limit    = 0  → défaut 50 (comportement interne)

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// ========================================================================
// makeActionLog — helper : crée un ActionLogEntry de test
// ========================================================================

func makeActionLog(id, event, hostname, actionType string, success bool) ActionLogEntry {
	return ActionLogEntry{
		ID:             id,
		Event:          event,
		Hostname:       hostname,
		ActionType:     actionType,
		ActionIndex:    0,
		ConfigSnapshot: fmt.Sprintf(`{"type":%q}`, actionType),
		Success:        success,
		Error:          "",
		DurationMs:     42,
		ExecutedAt:     time.Now().UTC(),
	}
}

// ========================================================================
// TestCreateActionLog
// Création d'un enregistrement action_log, puis lecture via ListActionLogs
// ========================================================================

func TestCreateActionLog(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	rec := makeActionLog("al-1", "host.new", "my-server-01", "webhook", true)
	if err := store.CreateActionLog(ctx, rec); err != nil {
		t.Fatalf("CreateActionLog: %v", err)
	}

	// Lecture sans filtre
	list, err := store.ListActionLogs(ctx, ActionLogFilter{})
	if err != nil {
		t.Fatalf("ListActionLogs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 record, got %d", len(list))
	}

	got := list[0]
	if got.Event != "host.new" {
		t.Errorf("Event: got %q, want host.new", got.Event)
	}
	if got.Hostname != "my-server-01" {
		t.Errorf("Hostname: got %q, want my-server-01", got.Hostname)
	}
	if got.ActionType != "webhook" {
		t.Errorf("ActionType: got %q, want webhook", got.ActionType)
	}
	if !got.Success {
		t.Errorf("Success: got false, want true")
	}
	if got.DurationMs != 42 {
		t.Errorf("DurationMs: got %d, want 42", got.DurationMs)
	}
	if got.ConfigSnapshot == "" {
		t.Error("ConfigSnapshot must not be empty")
	}
}

// ========================================================================
// TestListActionLogs_by_event
// Filtre ActionLogFilter{Event:"host.new"} — seuls les enregistrements
// de cet event sont retournés
// ========================================================================

func TestListActionLogs_by_event(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// 3 enregistrements : 2 × host.new, 1 × host.up
	for i, event := range []string{"host.new", "host.up", "host.new"} {
		rec := makeActionLog(fmt.Sprintf("al-%d", i), event, "h1", "file", true)
		if err := store.CreateActionLog(ctx, rec); err != nil {
			t.Fatalf("CreateActionLog %d: %v", i, err)
		}
	}

	// Filtre host.new → 2 résultats
	list, err := store.ListActionLogs(ctx, ActionLogFilter{Event: "host.new"})
	if err != nil {
		t.Fatalf("ListActionLogs host.new: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 records for host.new, got %d", len(list))
	}
	for _, rec := range list {
		if rec.Event != "host.new" {
			t.Errorf("unexpected event %q in filtered result", rec.Event)
		}
	}

	// Filtre host.up → 1 résultat
	list, err = store.ListActionLogs(ctx, ActionLogFilter{Event: "host.up"})
	if err != nil {
		t.Fatalf("ListActionLogs host.up: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 record for host.up, got %d", len(list))
	}

	// Filtre event vide → tous (3 résultats)
	list, err = store.ListActionLogs(ctx, ActionLogFilter{})
	if err != nil {
		t.Fatalf("ListActionLogs all events: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("expected 3 records for empty event filter, got %d", len(list))
	}
}

// ========================================================================
// TestListActionLogs_by_hostname
// Filtre ActionLogFilter{Hostname:"server-a"} — seuls les enregistrements
// de ce hostname sont retournés
// ========================================================================

func TestListActionLogs_by_hostname(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// 3 enregistrements : 2 × server-a, 1 × server-b
	for i, hostname := range []string{"server-a", "server-b", "server-a"} {
		rec := makeActionLog(fmt.Sprintf("al-%d", i), "host.new", hostname, "shell", true)
		if err := store.CreateActionLog(ctx, rec); err != nil {
			t.Fatalf("CreateActionLog %d: %v", i, err)
		}
	}

	// Filtre server-a → 2 résultats
	list, err := store.ListActionLogs(ctx, ActionLogFilter{Hostname: "server-a"})
	if err != nil {
		t.Fatalf("ListActionLogs server-a: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 records for server-a, got %d", len(list))
	}
	for _, rec := range list {
		if rec.Hostname != "server-a" {
			t.Errorf("unexpected hostname %q in filtered result", rec.Hostname)
		}
	}

	// Filtre server-b → 1 résultat
	list, err = store.ListActionLogs(ctx, ActionLogFilter{Hostname: "server-b"})
	if err != nil {
		t.Fatalf("ListActionLogs server-b: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("expected 1 record for server-b, got %d", len(list))
	}

	// Filtre combiné event + hostname
	list, err = store.ListActionLogs(ctx, ActionLogFilter{Event: "host.new", Hostname: "server-a"})
	if err != nil {
		t.Fatalf("ListActionLogs combined: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("expected 2 records for host.new+server-a, got %d", len(list))
	}
}

// ========================================================================
// TestListActionLogs_limit
// Le champ Limit dans ActionLogFilter borne le nombre de résultats
// ========================================================================

func TestListActionLogs_limit(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Insérer 7 enregistrements
	for i := 0; i < 7; i++ {
		rec := makeActionLog(fmt.Sprintf("al-%d", i), "host.new", "h1", "api", true)
		if err := store.CreateActionLog(ctx, rec); err != nil {
			t.Fatalf("CreateActionLog %d: %v", i, err)
		}
	}

	// Limit=3 → 3 résultats
	list, err := store.ListActionLogs(ctx, ActionLogFilter{Limit: 3})
	if err != nil {
		t.Fatalf("ListActionLogs Limit=3: %v", err)
	}
	if len(list) != 3 {
		t.Errorf("expected 3 records (Limit=3), got %d", len(list))
	}

	// Limit=10 → 7 résultats (tous)
	list, err = store.ListActionLogs(ctx, ActionLogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListActionLogs Limit=10: %v", err)
	}
	if len(list) != 7 {
		t.Errorf("expected 7 records (Limit=10, all), got %d", len(list))
	}

	// Limit=0 → limite par défaut (50) → tous les 7 retournés
	list, err = store.ListActionLogs(ctx, ActionLogFilter{Limit: 0})
	if err != nil {
		t.Fatalf("ListActionLogs Limit=0 (default): %v", err)
	}
	if len(list) < 7 {
		t.Errorf("expected at least 7 records for default limit, got %d", len(list))
	}
}
