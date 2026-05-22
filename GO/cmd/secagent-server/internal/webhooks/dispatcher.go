package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"secagent-server/cmd/secagent-server/internal/storage"
)

// ========================================================================
// Event types
// ========================================================================

// EventType identifies an agent lifecycle event.
type EventType string

const (
	EventHostNew     EventType = "host.new"
	EventHostUp      EventType = "host.up"
	EventHostDown    EventType = "host.down"
	EventHostRevoked EventType = "host.revoked"
	EventHostDeleted EventType = "host.deleted"
)

// AllEvents lists all valid event types (used for input validation).
var AllEvents = []EventType{
	EventHostNew, EventHostUp, EventHostDown,
	EventHostRevoked, EventHostDeleted,
}

// ========================================================================
// Payload types
// ========================================================================

// EventPayload is the JSON body POSTed to each webhook target URL.
type EventPayload struct {
	Event     string   `json:"event"`
	Timestamp string   `json:"timestamp"`
	Host      HostInfo `json:"host"`
}

// HostInfo carries the agent data associated with the event.
type HostInfo struct {
	Hostname   string `json:"hostname"`
	Status     string `json:"status,omitempty"`
	EnrolledAt string `json:"enrolled_at,omitempty"`
}

// ========================================================================
// WebhookStorer — interface subset used by Dispatcher (enables mocking)
// ========================================================================

// WebhookStorer is implemented by *storage.Store after task 11.1.
type WebhookStorer interface {
	ListWebhooksByEvent(ctx context.Context, event string) ([]storage.WebhookRecord, error)
	CreateDelivery(ctx context.Context, record storage.DeliveryRecord) error
}

// ========================================================================
// Dispatcher
// ========================================================================

// dispatchJob is a unit of work queued for asynchronous delivery.
type dispatchJob struct {
	eventType EventType
	payload   EventPayload
}

// Dispatcher receives events and delivers them to registered webhooks.
// Delivery is asynchronous: Dispatch() never blocks the caller.
type Dispatcher struct {
	store  WebhookStorer
	queue  chan dispatchJob
	client *http.Client
}

// GlobalDispatcher is the singleton injected from main.go.
// Handlers call webhooks.GlobalDispatcher.Dispatch(...) directly.
var GlobalDispatcher *Dispatcher

// DispatchFunc is injected from main.go into ws/handler.go to avoid
// circular imports (ws → webhooks → ... would cycle).
// Signature matches what ws/handler.go will call.
var DispatchFunc func(event string, hostname string, status string)

// NewDispatcher creates a Dispatcher with a buffered queue of queueSize.
// 3xx responses are not followed (CheckRedirect returns ErrUseLastResponse).
func NewDispatcher(store WebhookStorer, queueSize int) *Dispatcher {
	return &Dispatcher{
		store: store,
		queue: make(chan dispatchJob, queueSize),
		client: &http.Client{
			// No global timeout — each request uses a context.WithTimeout
			// derived from the webhook's timeout_seconds setting.
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				// Treat 3xx as an error: do not follow redirects.
				return http.ErrUseLastResponse
			},
		},
	}
}

// Start launches the background goroutine that drains the queue.
// Call once from main.go after NewDispatcher.
// Returns when ctx is cancelled; in-flight deliveries run to completion.
func (d *Dispatcher) Start(ctx context.Context) {
	go func() {
		slog.Info("webhook dispatcher started")
		for {
			select {
			case <-ctx.Done():
				slog.Info("webhook dispatcher stopped", "reason", ctx.Err())
				return
			case job, ok := <-d.queue:
				if !ok {
					slog.Info("webhook dispatcher: queue closed, exiting")
					return
				}
				d.fanOut(ctx, job)
			}
		}
	}()
}

// fanOut looks up all enabled webhooks for job.eventType and launches
// one goroutine per webhook for concurrent, independent delivery.
func (d *Dispatcher) fanOut(ctx context.Context, job dispatchJob) {
	webhooks, err := d.store.ListWebhooksByEvent(ctx, string(job.eventType))
	if err != nil {
		slog.Error("webhook dispatcher: list webhooks failed",
			"event", job.eventType, "error", err)
		return
	}
	if len(webhooks) == 0 {
		return // common path — no subscribers, silent
	}
	for _, wh := range webhooks {
		wh := wh // capture loop variable for goroutine
		go d.deliverToWebhook(wh, job.payload)
	}
}

// Dispatch enqueues an event for asynchronous delivery.
// It never blocks: if the queue is full the event is dropped with a warning.
func (d *Dispatcher) Dispatch(eventType EventType, payload EventPayload) {
	select {
	case d.queue <- dispatchJob{eventType: eventType, payload: payload}:
	default:
		slog.Warn("webhook queue full, dropping event",
			"event", string(eventType),
			"hostname", payload.Host.Hostname)
	}
}

// ========================================================================
// deliverToWebhook — retry logic with exponential back-off
// ========================================================================

// deliverToWebhook attempts to POST payload to wh.URL up to
// (wh.MaxRetries + 1) times with exponential back-off.
//
// Back-off schedule after attempt k fails (before attempt k+1):
//
//	sleep = min(2^(k-1), 60) seconds
//	k=1 → 1s, k=2 → 2s, k=3 → 4s, k=4+ → capped at 60s
//
// 4xx responses are permanent failures — no retry.
// 3xx responses are errors (redirect not followed).
func (d *Dispatcher) deliverToWebhook(wh storage.WebhookRecord, payload EventPayload) {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		slog.Error("webhook: marshal payload failed",
			"webhook_id", wh.ID, "event", payload.Event, "error", err)
		return
	}

	maxAttempts := wh.MaxRetries + 1

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		success, statusCode, errMsg, durationMs := d.attempt(wh, bodyBytes, payload.Event, attempt)

		// Log delivery regardless of outcome
		deliveryID := uuid.New().String()
		if logErr := d.store.CreateDelivery(context.Background(), storage.DeliveryRecord{
			ID:          deliveryID,
			WebhookID:   wh.ID,
			Event:       payload.Event,
			Payload:     string(bodyBytes),
			Attempt:     attempt,
			StatusCode:  statusCode,
			Success:     success,
			Error:       errMsg,
			AttemptedAt: time.Now().UTC(), // approximate — t0 captured inside attempt()
			DurationMs:  &durationMs,
		}); logErr != nil {
			slog.Error("webhook: CreateDelivery failed",
				"webhook_id", wh.ID, "delivery_id", deliveryID, "error", logErr)
		}

		if success {
			slog.Info("webhook delivered",
				"webhook_id", wh.ID, "event", payload.Event,
				"attempt", attempt, "status", *statusCode, "duration_ms", durationMs)
			return
		}

		// 4xx = permanent failure, no retry
		if statusCode != nil && *statusCode >= 400 && *statusCode < 500 {
			slog.Warn("webhook 4xx, no retry",
				"webhook_id", wh.ID, "event", payload.Event,
				"attempt", attempt, "status", *statusCode)
			return
		}

		slog.Warn("webhook delivery failed",
			"webhook_id", wh.ID, "event", payload.Event,
			"attempt", attempt, "error", errMsg)

		// Sleep before next attempt (only if there is one)
		if attempt <= wh.MaxRetries {
			sleep := backoffDuration(attempt)
			time.Sleep(sleep)
		}
	}

	slog.Error("webhook: all retries exhausted",
		"webhook_id", wh.ID, "event", payload.Event, "max_retries", wh.MaxRetries)
}

// attempt performs a single HTTP POST and returns (success, statusCode, errMsg, durationMs).
// statusCode is nil when a network error prevents receiving any response.
func (d *Dispatcher) attempt(
	wh storage.WebhookRecord,
	bodyBytes []byte,
	event string,
	attemptNum int,
) (success bool, statusCode *int, errMsg string, durationMs int64) {
	timeout := time.Duration(wh.TimeoutSeconds) * time.Second
	reqCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, wh.URL, bytes.NewReader(bodyBytes))
	if err != nil {
		return false, nil, fmt.Sprintf("build request: %v", err), 0
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "secagent-server/1.0")
	req.Header.Set("X-Event", event)
	req.Header.Set("X-Webhook-ID", wh.ID)
	if wh.Secret != "" {
		req.Header.Set("X-Signature", "sha256="+computeHMAC(bodyBytes, wh.Secret))
	}

	t0 := time.Now()
	resp, doErr := d.client.Do(req)
	durationMs = time.Since(t0).Milliseconds()

	if doErr != nil {
		return false, nil, doErr.Error(), durationMs
	}
	resp.Body.Close()

	sc := resp.StatusCode
	statusCode = &sc
	success = sc >= 200 && sc < 300
	if !success {
		errMsg = fmt.Sprintf("HTTP %d", sc)
	}
	return
}

// backoffDuration returns the sleep duration before attempt (k+1)
// after attempt k failed: min(2^(k-1), 60) seconds.
func backoffDuration(k int) time.Duration {
	shift := uint(k - 1)
	if shift > 5 { // 2^6=64>60, cap at 2^5=32 then check again
		shift = 5
	}
	secs := int64(1) << shift // 1, 2, 4, 8, 16, 32
	if secs > 60 {
		secs = 60
	}
	return time.Duration(secs) * time.Second
}

// ========================================================================
// HMAC-SHA256 signing
// ========================================================================

// computeHMAC returns the hex-encoded HMAC-SHA256 of body using secret as key.
// Used to populate the X-Signature header.
func computeHMAC(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
