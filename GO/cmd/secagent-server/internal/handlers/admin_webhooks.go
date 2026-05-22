package handlers

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"secagent-server/cmd/secagent-server/internal/storage"
	"secagent-server/cmd/secagent-server/internal/webhooks"
	"secagent-server/cmd/secagent-server/internal/ws"
)

// ========================================================================
// Request / Response types — webhooks
// ========================================================================

// WebhookCreateRequest is the body for POST /api/admin/webhooks.
type WebhookCreateRequest struct {
	URL            string   `json:"url"`
	Events         []string `json:"events"`
	Secret         string   `json:"secret"`
	MaxRetries     *int     `json:"max_retries"`     // pointer → nil means "use default 3"
	TimeoutSeconds *int     `json:"timeout_seconds"` // pointer → nil means "use default 10"
	Description    string   `json:"description"`
}

// WebhookResponse is the public view of a webhook subscription.
// The secret field is always returned as "" to avoid exposing HMAC keys.
type WebhookResponse struct {
	ID             string   `json:"id"`
	URL            string   `json:"url"`
	Events         []string `json:"events"`
	Secret         string   `json:"secret"` // always "" — never expose
	MaxRetries     int      `json:"max_retries"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Enabled        bool     `json:"enabled"`
	CreatedAt      string   `json:"created_at"`
	Description    string   `json:"description,omitempty"`
}

// DeliveryResponse is the public view of a webhook delivery attempt.
type DeliveryResponse struct {
	ID          string `json:"id"`
	WebhookID   string `json:"webhook_id"`
	Event       string `json:"event"`
	Payload     string `json:"payload"`
	Attempt     int    `json:"attempt"`
	StatusCode  *int   `json:"status_code"`  // null if connection error
	Success     bool   `json:"success"`
	Error       string `json:"error"`
	AttemptedAt string `json:"attempted_at"`
	DurationMs  *int64 `json:"duration_ms"`  // null if not measurable
}

// ========================================================================
// Helpers
// ========================================================================

func webhookRecordToResponse(rec storage.WebhookRecord) WebhookResponse {
	events := rec.Events
	if events == nil {
		events = []string{}
	}
	return WebhookResponse{
		ID:             rec.ID,
		URL:            rec.URL,
		Events:         events,
		Secret:         "", // never expose
		MaxRetries:     rec.MaxRetries,
		TimeoutSeconds: rec.TimeoutSeconds,
		Enabled:        rec.Enabled,
		CreatedAt:      rec.CreatedAt.UTC().Format(time.RFC3339),
		Description:    rec.Description,
	}
}

func deliveryRecordToResponse(rec storage.DeliveryRecord) DeliveryResponse {
	return DeliveryResponse{
		ID:          rec.ID,
		WebhookID:   rec.WebhookID,
		Event:       rec.Event,
		Payload:     rec.Payload,
		Attempt:     rec.Attempt,
		StatusCode:  rec.StatusCode,
		Success:     rec.Success,
		Error:       rec.Error,
		AttemptedAt: rec.AttemptedAt.UTC().Format(time.RFC3339),
		DurationMs:  rec.DurationMs,
	}
}

// isValidWebhookEvent returns true when ev is one of the 5 known event types.
func isValidWebhookEvent(ev string) bool {
	for _, e := range webhooks.AllEvents {
		if string(e) == ev {
			return true
		}
	}
	return false
}

// ========================================================================
// POST /api/admin/webhooks
// ========================================================================

// AdminCreateWebhook creates a new webhook subscription.
// Validates url, events, max_retries, timeout_seconds before inserting.
func AdminCreateWebhook(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAuth(w, r) {
		return
	}

	var req WebhookCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	defer r.Body.Close()

	// --- Validate url ---
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing_url"})
		return
	}
	if !strings.HasPrefix(req.URL, "http://") && !strings.HasPrefix(req.URL, "https://") {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":  "invalid_url",
			"detail": "url must start with http:// or https://",
		})
		return
	}

	// --- Validate events ---
	if len(req.Events) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing_events"})
		return
	}
	for _, ev := range req.Events {
		if !isValidWebhookEvent(ev) {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"error": "invalid_event",
				"event": ev,
			})
			return
		}
	}

	// --- Validate max_retries (default 3, range 0–10) ---
	maxRetries := 3
	if req.MaxRetries != nil {
		maxRetries = *req.MaxRetries
		if maxRetries < 0 || maxRetries > 10 {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"error":  "invalid_max_retries",
				"detail": "must be between 0 and 10",
			})
			return
		}
	}

	// --- Validate timeout_seconds (default 10, range 1–60) ---
	timeoutSeconds := 10
	if req.TimeoutSeconds != nil {
		timeoutSeconds = *req.TimeoutSeconds
		if timeoutSeconds < 1 || timeoutSeconds > 60 {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"error":  "invalid_timeout_seconds",
				"detail": "must be between 1 and 60",
			})
			return
		}
	}

	if adminStore == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store_not_initialized"})
		return
	}

	id := uuid.New().String()
	now := time.Now().UTC()

	rec := storage.WebhookRecord{
		ID:             id,
		URL:            req.URL,
		Events:         req.Events,
		Secret:         req.Secret,
		MaxRetries:     maxRetries,
		TimeoutSeconds: timeoutSeconds,
		Enabled:        true,
		CreatedAt:      now,
		Description:    req.Description,
	}

	if err := adminStore.CreateWebhook(r.Context(), rec); err != nil {
		log.Printf("AdminCreateWebhook: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}

	log.Printf("Webhook created: id=%s url=%s events=%v", id, req.URL, req.Events)
	writeJSON(w, http.StatusCreated, webhookRecordToResponse(rec))
}

// ========================================================================
// GET /api/admin/webhooks
// ========================================================================

// AdminListWebhooks returns all registered webhooks.
// Always returns [] (never null) when no webhooks exist.
func AdminListWebhooks(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAuth(w, r) {
		return
	}
	if adminStore == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store_not_initialized"})
		return
	}

	records, err := adminStore.ListWebhooks(r.Context())
	if err != nil {
		log.Printf("AdminListWebhooks: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}

	result := make([]WebhookResponse, 0, len(records))
	for _, rec := range records {
		result = append(result, webhookRecordToResponse(rec))
	}
	writeJSON(w, http.StatusOK, result)
}

// ========================================================================
// GET /api/admin/webhooks/{id}
// ========================================================================

// AdminGetWebhook returns the detail of a single webhook.
func AdminGetWebhook(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAuth(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing_id"})
		return
	}
	if adminStore == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store_not_initialized"})
		return
	}

	rec, err := adminStore.GetWebhook(r.Context(), id)
	if err != nil {
		log.Printf("AdminGetWebhook: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	if rec == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "webhook_not_found"})
		return
	}

	writeJSON(w, http.StatusOK, webhookRecordToResponse(*rec))
}

// ========================================================================
// DELETE /api/admin/webhooks/{id}
// ========================================================================

// AdminDeleteWebhook permanently removes a webhook and its delivery log (CASCADE).
func AdminDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAuth(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing_id"})
		return
	}
	if adminStore == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store_not_initialized"})
		return
	}

	deleted, err := adminStore.DeleteWebhook(r.Context(), id)
	if err != nil {
		log.Printf("AdminDeleteWebhook: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	if !deleted {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "webhook_not_found"})
		return
	}

	log.Printf("Webhook deleted: id=%s", id)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "deleted"})
}

// ========================================================================
// GET /api/admin/webhooks/{id}/deliveries
// ========================================================================

// AdminListDeliveries returns the delivery history for a webhook.
// Query param: limit (default 50, max 200).
func AdminListDeliveries(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAuth(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing_id"})
		return
	}
	if adminStore == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store_not_initialized"})
		return
	}

	// Parse and validate limit
	limit := 50
	if lStr := r.URL.Query().Get("limit"); lStr != "" {
		n, err := strconv.Atoi(lStr)
		if err != nil || n < 1 || n > 200 {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"error":  "invalid_limit",
				"detail": "must be between 1 and 200",
			})
			return
		}
		limit = n
	}

	ctx := r.Context()

	// Verify webhook exists
	wh, err := adminStore.GetWebhook(ctx, id)
	if err != nil {
		log.Printf("AdminListDeliveries GetWebhook: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	if wh == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "webhook_not_found"})
		return
	}

	records, err := adminStore.ListDeliveries(ctx, id, limit)
	if err != nil {
		log.Printf("AdminListDeliveries: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}

	result := make([]DeliveryResponse, 0, len(records))
	for _, rec := range records {
		result = append(result, deliveryRecordToResponse(rec))
	}
	writeJSON(w, http.StatusOK, result)
}

// ========================================================================
// DELETE /api/admin/minions/{hostname}
// ========================================================================

// AdminDeleteMinion permanently removes an agent from the DB.
// Closes the WS connection (code 4000) if active, then dispatches host.deleted.
// Distinct from POST /api/admin/revoke/{hostname} which blacklists but does not delete.
func AdminDeleteMinion(w http.ResponseWriter, r *http.Request) {
	if !requireAdminAuth(w, r) {
		return
	}

	hostname := r.PathValue("hostname")
	if hostname == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing_hostname"})
		return
	}
	if adminStore == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store_not_initialized"})
		return
	}

	ctx := r.Context()

	// Verify agent exists
	agent, err := adminStore.GetAgent(ctx, hostname)
	if err != nil {
		log.Printf("AdminDeleteMinion GetAgent: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	if agent == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent_not_found"})
		return
	}

	// Close active WS connection with code 4000 (normal close)
	wsDisconnected := ws.CloseAgent(hostname, ws.WSCloseNormal, "admin_delete")

	// Delete from DB (agents + authorized_keys best-effort)
	deleted, err := adminStore.DeleteAgent(ctx, hostname)
	if err != nil {
		log.Printf("AdminDeleteMinion DeleteAgent: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "db_error"})
		return
	}
	if !deleted {
		// Race: another request deleted it between GetAgent and DeleteAgent
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent_not_found"})
		return
	}

	// Dispatch host.deleted event asynchronously (nil-safe: nil during tests)
	if webhooks.GlobalDispatcher != nil {
		webhooks.GlobalDispatcher.Dispatch(webhooks.EventHostDeleted, webhooks.EventPayload{
			Event:     string(webhooks.EventHostDeleted),
			Timestamp: time.Now().UTC().Format(time.RFC3339),
			Host: webhooks.HostInfo{
				Hostname: hostname,
				Status:   "deleted",
			},
		})
	}

	log.Printf("Minion deleted: hostname=%s ws_disconnected=%v", hostname, wsDisconnected)
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"hostname":        hostname,
		"status":          "deleted",
		"ws_disconnected": wsDisconnected,
	})
}
