package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/events"
)

// WebhookStore persists the event-delivery registry, delivery status, and relay
// cursor (PRS-018). It is additive infrastructure — it never reads or writes the
// governance or audit tables. It satisfies the events package's persistence ports.
type WebhookStore struct {
	pool *pgxpool.Pool
}

// NewWebhookStore builds a store over the pool.
func NewWebhookStore(pool *pgxpool.Pool) *WebhookStore { return &WebhookStore{pool: pool} }

var (
	_ events.WebhookStore  = (*WebhookStore)(nil)
	_ events.DeliveryStore = (*WebhookStore)(nil)
	_ events.CursorStore   = (*WebhookStore)(nil)
)

const webhookCols = `id, url, secret, enabled, operations, resources, max_retries, created_at, updated_at`

// Create inserts a webhook.
func (s *WebhookStore) Create(ctx context.Context, w events.Webhook) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO webhook (`+webhookCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now(),now())`,
		w.ID, w.URL, w.Secret, w.Enabled, textArray(w.Operations), textArray(w.Resources), w.MaxRetries)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return fmt.Errorf("create webhook: %w", err)
	}
	return nil
}

// Get returns a webhook or domain.ErrNotFound.
func (s *WebhookStore) Get(ctx context.Context, id string) (events.Webhook, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+webhookCols+` FROM webhook WHERE id = $1`, id)
	w, err := scanWebhook(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return events.Webhook{}, domain.ErrNotFound
	}
	if err != nil {
		return events.Webhook{}, fmt.Errorf("get webhook: %w", err)
	}
	return w, nil
}

// List returns all webhooks.
func (s *WebhookStore) List(ctx context.Context) ([]events.Webhook, error) {
	return s.query(ctx, `SELECT `+webhookCols+` FROM webhook ORDER BY created_at`)
}

// ListEnabled returns only enabled webhooks.
func (s *WebhookStore) ListEnabled(ctx context.Context) ([]events.Webhook, error) {
	return s.query(ctx, `SELECT `+webhookCols+` FROM webhook WHERE enabled ORDER BY created_at`)
}

func (s *WebhookStore) query(ctx context.Context, sql string) ([]events.Webhook, error) {
	rows, err := s.pool.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("list webhooks: %w", err)
	}
	defer rows.Close()
	var out []events.Webhook
	for rows.Next() {
		w, err := scanWebhook(rows)
		if err != nil {
			return nil, fmt.Errorf("scan webhook: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// Update replaces a webhook's mutable fields (URL, secret, enabled, filters,
// retries). Secret rotation goes through here.
func (s *WebhookStore) Update(ctx context.Context, w events.Webhook) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE webhook
		   SET url=$2, secret=$3, enabled=$4, operations=$5, resources=$6,
		       max_retries=$7, updated_at=now()
		 WHERE id=$1`,
		w.ID, w.URL, w.Secret, w.Enabled, textArray(w.Operations), textArray(w.Resources), w.MaxRetries)
	if err != nil {
		return fmt.Errorf("update webhook: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Delete removes a webhook (its delivery history is retained).
func (s *WebhookStore) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM webhook WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete webhook: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

const deliveryCols = `id, webhook_id, chain_id, seq, event_id, operation_id, operation, actor,
	cfg_id, occurred_at, status, retry_count, last_status_code, last_attempt, next_attempt_at, created_at`

// Enqueue inserts pending deliveries. Duplicate ids are ignored (idempotent relay).
func (s *WebhookStore) Enqueue(ctx context.Context, ds []events.Delivery) error {
	batch := &pgx.Batch{}
	for i := range ds {
		d := ds[i]
		batch.Queue(`
			INSERT INTO webhook_delivery
				(id, webhook_id, chain_id, seq, event_id, operation_id, operation, actor,
				 cfg_id, occurred_at, status, retry_count, next_attempt_at, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,0,$12,now())
			ON CONFLICT (id) DO NOTHING`,
			d.ID, d.WebhookID, d.Event.ChainID, d.Event.Seq, d.Event.EventID, d.Event.OperationID,
			d.Event.Operation, d.Event.Actor, d.Event.CfgID, d.Event.OccurredAt, string(d.Status), d.NextAttemptAt)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range ds {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("enqueue delivery: %w", err)
		}
	}
	return nil
}

// ClaimDue returns deliveries eligible for a send attempt (pending/failed and due).
func (s *WebhookStore) ClaimDue(ctx context.Context, now time.Time, limit int) ([]events.Delivery, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+deliveryCols+`
		  FROM webhook_delivery
		 WHERE status IN ('pending','failed') AND next_attempt_at <= $1
		 ORDER BY next_attempt_at
		 LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claim deliveries: %w", err)
	}
	defer rows.Close()
	var out []events.Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("scan delivery: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkResult records the outcome of a delivery attempt.
func (s *WebhookStore) MarkResult(ctx context.Context, id string, status events.DeliveryStatus, retry, code int, last, next time.Time) error {
	var lastPtr, nextPtr *time.Time
	if !last.IsZero() {
		lastPtr = &last
	}
	if !next.IsZero() {
		nextPtr = &next
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE webhook_delivery
		   SET status=$2, retry_count=$3, last_status_code=$4, last_attempt=$5,
		       next_attempt_at=COALESCE($6, next_attempt_at)
		 WHERE id=$1`,
		id, string(status), retry, code, lastPtr, nextPtr)
	if err != nil {
		return fmt.Errorf("mark delivery: %w", err)
	}
	return nil
}

// ListByWebhook returns a webhook's recent deliveries (newest first).
func (s *WebhookStore) ListByWebhook(ctx context.Context, webhookID string, limit int) ([]events.Delivery, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+deliveryCols+` FROM webhook_delivery
		 WHERE webhook_id=$1 ORDER BY seq DESC LIMIT $2`, webhookID, limit)
	if err != nil {
		return nil, fmt.Errorf("list deliveries: %w", err)
	}
	defer rows.Close()
	var out []events.Delivery
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			return nil, fmt.Errorf("scan delivery: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Get returns the relay cursor for a chain (0 when absent).
func (s *WebhookStore) GetCursor(ctx context.Context, chainID string) (int64, error) {
	var seq int64
	err := s.pool.QueryRow(ctx, `SELECT last_seq FROM webhook_cursor WHERE chain_id=$1`, chainID).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get cursor: %w", err)
	}
	return seq, nil
}

// Set advances the relay cursor for a chain.
func (s *WebhookStore) SetCursor(ctx context.Context, chainID string, seq int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO webhook_cursor (chain_id, last_seq, updated_at)
		VALUES ($1,$2,now())
		ON CONFLICT (chain_id) DO UPDATE SET last_seq=EXCLUDED.last_seq, updated_at=now()`,
		chainID, seq)
	if err != nil {
		return fmt.Errorf("set cursor: %w", err)
	}
	return nil
}

func scanWebhook(sc rowScanner) (events.Webhook, error) {
	var w events.Webhook
	err := sc.Scan(&w.ID, &w.URL, &w.Secret, &w.Enabled, &w.Operations, &w.Resources,
		&w.MaxRetries, &w.CreatedAt, &w.UpdatedAt)
	return w, err
}

func scanDelivery(sc rowScanner) (events.Delivery, error) {
	var d events.Delivery
	var op, status string
	var lastAttempt *time.Time
	if err := sc.Scan(
		&d.ID, &d.WebhookID, &d.Event.ChainID, &d.Event.Seq, &d.Event.EventID, &d.Event.OperationID,
		&op, &d.Event.Actor, &d.Event.CfgID, &d.Event.OccurredAt, &status, &d.RetryCount,
		&d.LastStatusCode, &lastAttempt, &d.NextAttemptAt, &d.CreatedAt,
	); err != nil {
		return events.Delivery{}, err
	}
	d.Event.Operation = op
	d.Status = events.DeliveryStatus(status)
	if lastAttempt != nil {
		d.LastAttempt = *lastAttempt
	}
	return d, nil
}
