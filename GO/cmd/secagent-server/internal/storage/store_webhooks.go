package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// WebhookRecord represents a row in the webhooks table.
type WebhookRecord struct {
	ID             string
	URL            string
	Events         []string // stored as JSON array in DB
	Secret         string
	MaxRetries     int
	TimeoutSeconds int
	Enabled        bool
	CreatedAt      time.Time
	Description    string
}

// DeliveryRecord represents a row in the webhook_deliveries table.
type DeliveryRecord struct {
	ID          string
	WebhookID   string
	Event       string
	Payload     string
	Attempt     int
	StatusCode  *int   // nil if connection error
	Success     bool
	Error       string
	AttemptedAt time.Time
	DurationMs  *int64 // nil if not measurable
}

// ========================================================================
// webhooks — CRUD
// ========================================================================

// CreateWebhook inserts a new webhook subscription.
// events is serialised as a JSON array in the DB.
func (s *Store) CreateWebhook(ctx context.Context, rec WebhookRecord) error {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()

	eventsJSON, err := json.Marshal(rec.Events)
	if err != nil {
		return fmt.Errorf("CreateWebhook: marshal events: %w", err)
	}

	enabled := 0
	if rec.Enabled {
		enabled = 1
	}

	createdAt := rec.CreatedAt.UTC().Unix()

	_, err = s.db.ExecContext(ctx, `
		INSERT INTO webhooks
			(id, url, events, secret, max_retries, timeout_seconds, enabled, created_at, description)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.ID, rec.URL, string(eventsJSON), rec.Secret,
		rec.MaxRetries, rec.TimeoutSeconds, enabled, createdAt, rec.Description)
	if err != nil {
		return fmt.Errorf("CreateWebhook: %w", err)
	}

	log.Printf("Webhook created: id=%s url=%s events=%s", rec.ID, rec.URL, string(eventsJSON))
	return nil
}

// GetWebhook retrieves a webhook by ID.
// Returns (nil, nil) if the ID does not exist.
func (s *Store) GetWebhook(ctx context.Context, id string) (*WebhookRecord, error) {
	s.dbMu.RLock()
	defer s.dbMu.RUnlock()

	row := s.db.QueryRowContext(ctx, `
		SELECT id, url, events, secret, max_retries, timeout_seconds, enabled, created_at, description
		FROM webhooks WHERE id = ?
	`, id)

	return scanWebhookRow(row)
}

// ListWebhooks returns all webhook subscriptions, ordered by created_at DESC.
func (s *Store) ListWebhooks(ctx context.Context) ([]WebhookRecord, error) {
	s.dbMu.RLock()
	defer s.dbMu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url, events, secret, max_retries, timeout_seconds, enabled, created_at, description
		FROM webhooks
		ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("ListWebhooks: %w", err)
	}
	defer rows.Close()

	var webhooks []WebhookRecord
	for rows.Next() {
		rec, err := scanWebhookRows(rows)
		if err != nil {
			return nil, fmt.Errorf("ListWebhooks scan: %w", err)
		}
		webhooks = append(webhooks, *rec)
	}
	return webhooks, rows.Err()
}

// DeleteWebhook removes a webhook and its deliveries (via ON DELETE CASCADE).
// Returns (true, nil) if deleted, (false, nil) if not found.
func (s *Store) DeleteWebhook(ctx context.Context, id string) (bool, error) {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()

	result, err := s.db.ExecContext(ctx, "DELETE FROM webhooks WHERE id = ?", id)
	if err != nil {
		return false, fmt.Errorf("DeleteWebhook: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("DeleteWebhook rows: %w", err)
	}

	deleted := affected > 0
	if deleted {
		log.Printf("Webhook deleted: id=%s", id)
	}
	return deleted, nil
}

// ListWebhooksByEvent returns all enabled webhooks that subscribe to the given event.
// Uses json_each to perform a JSON-safe membership test on the events column.
func (s *Store) ListWebhooksByEvent(ctx context.Context, event string) ([]WebhookRecord, error) {
	s.dbMu.RLock()
	defer s.dbMu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, url, events, secret, max_retries, timeout_seconds, enabled, created_at, description
		FROM webhooks
		WHERE enabled = 1
		  AND EXISTS (SELECT 1 FROM json_each(webhooks.events) WHERE value = ?)
	`, event)
	if err != nil {
		return nil, fmt.Errorf("ListWebhooksByEvent: %w", err)
	}
	defer rows.Close()

	var webhooks []WebhookRecord
	for rows.Next() {
		rec, err := scanWebhookRows(rows)
		if err != nil {
			return nil, fmt.Errorf("ListWebhooksByEvent scan: %w", err)
		}
		webhooks = append(webhooks, *rec)
	}
	return webhooks, rows.Err()
}

// ========================================================================
// webhook_deliveries — log
// ========================================================================

// CreateDelivery logs a single webhook delivery attempt.
func (s *Store) CreateDelivery(ctx context.Context, rec DeliveryRecord) error {
	s.dbMu.Lock()
	defer s.dbMu.Unlock()

	success := 0
	if rec.Success {
		success = 1
	}

	attemptedAt := rec.AttemptedAt.UTC().Unix()

	var statusCode interface{}
	if rec.StatusCode != nil {
		statusCode = *rec.StatusCode
	}

	var durationMs interface{}
	if rec.DurationMs != nil {
		durationMs = *rec.DurationMs
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO webhook_deliveries
			(id, webhook_id, event, payload, attempt, status_code, success, error, attempted_at, duration_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.ID, rec.WebhookID, rec.Event, rec.Payload,
		rec.Attempt, statusCode, success, rec.Error, attemptedAt, durationMs)
	if err != nil {
		return fmt.Errorf("CreateDelivery: %w", err)
	}

	return nil
}

// ListDeliveries returns delivery attempts for a webhook ordered by attempted_at DESC.
// limit must be capped by the caller (handler caps at 200).
func (s *Store) ListDeliveries(ctx context.Context, webhookID string, limit int) ([]DeliveryRecord, error) {
	s.dbMu.RLock()
	defer s.dbMu.RUnlock()

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, webhook_id, event, payload, attempt, status_code, success, error, attempted_at, duration_ms
		FROM webhook_deliveries
		WHERE webhook_id = ?
		ORDER BY attempted_at DESC
		LIMIT ?
	`, webhookID, limit)
	if err != nil {
		return nil, fmt.Errorf("ListDeliveries: %w", err)
	}
	defer rows.Close()

	var deliveries []DeliveryRecord
	for rows.Next() {
		var rec DeliveryRecord
		var success int
		var statusCode sql.NullInt64
		var durationMs sql.NullInt64
		var attemptedAtUnix int64

		if err := rows.Scan(
			&rec.ID, &rec.WebhookID, &rec.Event, &rec.Payload,
			&rec.Attempt, &statusCode, &success, &rec.Error,
			&attemptedAtUnix, &durationMs,
		); err != nil {
			return nil, fmt.Errorf("ListDeliveries scan: %w", err)
		}

		rec.Success = success != 0
		rec.AttemptedAt = time.Unix(attemptedAtUnix, 0).UTC()
		if statusCode.Valid {
			v := int(statusCode.Int64)
			rec.StatusCode = &v
		}
		if durationMs.Valid {
			rec.DurationMs = &durationMs.Int64
		}

		deliveries = append(deliveries, rec)
	}
	return deliveries, rows.Err()
}

// ========================================================================
// internal scan helpers
// ========================================================================

func scanWebhookRow(row *sql.Row) (*WebhookRecord, error) {
	var rec WebhookRecord
	var eventsJSON string
	var enabled int
	var createdAtUnix int64
	var secret sql.NullString
	var description sql.NullString

	err := row.Scan(
		&rec.ID, &rec.URL, &eventsJSON, &secret,
		&rec.MaxRetries, &rec.TimeoutSeconds, &enabled,
		&createdAtUnix, &description,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan webhook: %w", err)
	}

	if err := json.Unmarshal([]byte(eventsJSON), &rec.Events); err != nil {
		return nil, fmt.Errorf("scan webhook: unmarshal events: %w", err)
	}
	rec.Enabled = enabled != 0
	rec.CreatedAt = time.Unix(createdAtUnix, 0).UTC()
	if secret.Valid {
		rec.Secret = secret.String
	}
	if description.Valid {
		rec.Description = description.String
	}

	return &rec, nil
}

func scanWebhookRows(rows *sql.Rows) (*WebhookRecord, error) {
	var rec WebhookRecord
	var eventsJSON string
	var enabled int
	var createdAtUnix int64
	var secret sql.NullString
	var description sql.NullString

	err := rows.Scan(
		&rec.ID, &rec.URL, &eventsJSON, &secret,
		&rec.MaxRetries, &rec.TimeoutSeconds, &enabled,
		&createdAtUnix, &description,
	)
	if err != nil {
		return nil, fmt.Errorf("scan webhook row: %w", err)
	}

	if err := json.Unmarshal([]byte(eventsJSON), &rec.Events); err != nil {
		return nil, fmt.Errorf("scan webhook row: unmarshal events: %w", err)
	}
	rec.Enabled = enabled != 0
	rec.CreatedAt = time.Unix(createdAtUnix, 0).UTC()
	if secret.Valid {
		rec.Secret = secret.String
	}
	if description.Valid {
		rec.Description = description.String
	}

	return &rec, nil
}
