// Phase 12 — push_manager.go
// PushManager polls each push-mode relay's inventory and keeps relay_routing up to date.
//
// For each push relay registered in DB:
//  1. Call GET /api/inventory on the relay (every PollInterval, default 30s)
//  2. Extract hostnames
//  3. Call BulkUpsertRelayRouting to update relay_routing
//  4. Update relay status in DB (connected / disconnected)
//
// On failure, uses exponential backoff (max 60s) before retrying.
package proxy

import (
	"context"
	"log"
	"sync"
	"time"

	"secagent-server/cmd/secagent-server/internal/storage"
)

const (
	// DefaultPollInterval is how often push relays are polled for inventory.
	DefaultPollInterval = 30 * time.Second

	// maxBackoff is the maximum retry delay when a relay is unreachable.
	maxBackoff = 60 * time.Second
)

// PushManager periodically polls push-mode relays and keeps relay_routing in sync.
type PushManager struct {
	store        *storage.Store
	pollInterval time.Duration
	mu           sync.Mutex
	clients      map[string]*relayPollState // relay_id → poll state
}

// relayPollState tracks the polling state for a single push relay.
type relayPollState struct {
	client  *RelayClient
	backoff time.Duration
	cancel  context.CancelFunc
}

// NewPushManager creates a PushManager backed by the given store.
// pollInterval sets the inventory poll frequency (0 = DefaultPollInterval).
func NewPushManager(store *storage.Store, pollInterval time.Duration) *PushManager {
	if pollInterval <= 0 {
		pollInterval = DefaultPollInterval
	}
	return &PushManager{
		store:        store,
		pollInterval: pollInterval,
		clients:      make(map[string]*relayPollState),
	}
}

// Start launches the push manager in a goroutine.
// It discovers push relays from the DB and starts a polling loop for each.
// Blocks until ctx is cancelled.
func (m *PushManager) Start(ctx context.Context) {
	log.Printf("[PROXY] PushManager started (poll_interval=%s)", m.pollInterval)
	m.refreshRelays(ctx)

	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.stopAll()
			log.Println("[PROXY] PushManager stopped")
			return
		case <-ticker.C:
			m.refreshRelays(ctx)
		}
	}
}

// refreshRelays re-reads push relays from DB and starts/stops poll goroutines accordingly.
func (m *PushManager) refreshRelays(ctx context.Context) {
	nodes, err := m.store.ListRelayNodes()
	if err != nil {
		log.Printf("[PROXY] PushManager ListRelayNodes error: %v", err)
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	activeIDs := make(map[string]bool)
	for _, node := range nodes {
		if node.Mode != "push" {
			continue
		}
		activeIDs[node.RelayID] = true

		if _, running := m.clients[node.RelayID]; !running {
			// Start a new polling goroutine for this relay
			// For push mode, token_hash stores the plain token (Sprint 2 design)
			client := NewRelayClient(node.RelayID, node.URL, node.TokenHash)
			childCtx, cancel := context.WithCancel(ctx)
			state := &relayPollState{
				client:  client,
				backoff: 5 * time.Second,
				cancel:  cancel,
			}
			m.clients[node.RelayID] = state
			go m.pollLoop(childCtx, state)
			log.Printf("[PROXY] PushManager: started polling relay_id=%s url=%s", node.RelayID, node.URL)
		}
	}

	// Stop goroutines for relays no longer in DB
	for relayID, state := range m.clients {
		if !activeIDs[relayID] {
			state.cancel()
			delete(m.clients, relayID)
			log.Printf("[PROXY] PushManager: stopped polling relay_id=%s (removed from DB)", relayID)
		}
	}
}

// pollLoop polls one relay in a dedicated goroutine.
func (m *PushManager) pollLoop(ctx context.Context, state *relayPollState) {
	// Initial poll immediately
	m.pollOnce(ctx, state)

	ticker := time.NewTicker(m.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.pollOnce(ctx, state)
		}
	}
}

// pollOnce executes a single inventory poll for a relay.
// On failure, marks the relay disconnected and applies exponential backoff.
func (m *PushManager) pollOnce(ctx context.Context, state *relayPollState) {
	relayID := state.client.RelayID

	agents, err := state.client.GetInventory(ctx)
	if err != nil {
		log.Printf("[PROXY] PushManager poll error: relay_id=%s err=%v (backoff=%s)",
			relayID, err, state.backoff)
		// Mark relay as disconnected
		_ = m.store.UpdateRelayStatus(relayID, "disconnected", time.Now().Unix())
		// Back off before the next ticker fires
		applyBackoff(ctx, state)
		return
	}

	// Success — reset backoff
	state.backoff = 5 * time.Second

	// Extract hostnames
	hostnames := make([]string, 0, len(agents))
	for _, a := range agents {
		hostnames = append(hostnames, a.Hostname)
	}

	// Update relay_routing
	if err := m.store.BulkUpsertRelayRouting(relayID, hostnames); err != nil {
		log.Printf("[PROXY] PushManager routing update error: relay_id=%s err=%v", relayID, err)
	}

	// Mark relay as connected
	_ = m.store.UpdateRelayStatus(relayID, "connected", time.Now().Unix())

	log.Printf("[PROXY] PushManager poll ok: relay_id=%s agents=%d", relayID, len(hostnames))
}

// applyBackoff sleeps for state.backoff duration (respecting ctx) then doubles it.
func applyBackoff(ctx context.Context, state *relayPollState) {
	select {
	case <-ctx.Done():
	case <-time.After(state.backoff):
	}
	state.backoff *= 2
	if state.backoff > maxBackoff {
		state.backoff = maxBackoff
	}
}

// stopAll cancels all polling goroutines.
func (m *PushManager) stopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, state := range m.clients {
		state.cancel()
	}
	m.clients = make(map[string]*relayPollState)
}

// ActiveRelayCount returns the number of push relays currently being polled.
func (m *PushManager) ActiveRelayCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.clients)
}
