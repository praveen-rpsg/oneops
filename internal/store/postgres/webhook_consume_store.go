package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/rpsg/oneops/internal/events"
)

// This file adds the read/admin and replay-job persistence for PRS-019. It is
// additive to WebhookStore (PRS-018) — it changes no existing method and touches
// no governance or audit table.

var (
	_ events.DeliveryOps    = (*WebhookStore)(nil)
	_ events.ReplayJobStore = (*WebhookStore)(nil)
)

// GetDelivery returns a single delivery by id.
func (s *WebhookStore) GetDelivery(ctx context.Context, id string) (events.Delivery, bool, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+deliveryCols+` FROM webhook_delivery WHERE id=$1`, id)
	d, err := scanDelivery(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return events.Delivery{}, false, nil
		}
		return events.Delivery{}, false, fmt.Errorf("get delivery: %w", err)
	}
	return d, true, nil
}

// Requeue resets the given deliveries to pending and due-now, so the existing
// dispatcher re-attempts them. It returns the number of rows affected.
func (s *WebhookStore) Requeue(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE webhook_delivery
		   SET status='pending', retry_count=0, next_attempt_at=now()
		 WHERE id = ANY($1)`, ids)
	if err != nil {
		return 0, fmt.Errorf("requeue deliveries: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// RequeueDeadLetters resets dead-letter deliveries to pending. An empty webhookID
// requeues dead-letters across all webhooks.
func (s *WebhookStore) RequeueDeadLetters(ctx context.Context, webhookID string) (int, error) {
	sql := `UPDATE webhook_delivery SET status='pending', retry_count=0, next_attempt_at=now() WHERE status='dead_letter'`
	args := []any{}
	if webhookID != "" {
		sql += ` AND webhook_id=$1`
		args = append(args, webhookID)
	}
	tag, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return 0, fmt.Errorf("requeue dead-letters: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ListDeadLetters lists dead-letter deliveries (optionally for one webhook).
func (s *WebhookStore) ListDeadLetters(ctx context.Context, webhookID string, limit int) ([]events.Delivery, error) {
	sql := `SELECT ` + deliveryCols + ` FROM webhook_delivery WHERE status='dead_letter'`
	args := []any{}
	if webhookID != "" {
		sql += ` AND webhook_id=$1`
		args = append(args, webhookID)
	}
	sql += fmt.Sprintf(` ORDER BY seq DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list dead-letters: %w", err)
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

// DeleteOlderThan removes deliveries in the given terminal statuses created before
// cutoff. Callers pass only terminal statuses — pending/failed are never deleted.
func (s *WebhookStore) DeleteOlderThan(ctx context.Context, cutoff time.Time, statuses []events.DeliveryStatus) (int, error) {
	if len(statuses) == 0 {
		return 0, nil
	}
	ss := make([]string, len(statuses))
	for i, st := range statuses {
		ss[i] = string(st)
	}
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM webhook_delivery WHERE created_at < $1 AND status = ANY($2)`, cutoff, ss)
	if err != nil {
		return 0, fmt.Errorf("delete old deliveries: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// CountByStatus counts deliveries in a status.
func (s *WebhookStore) CountByStatus(ctx context.Context, status events.DeliveryStatus) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM webhook_delivery WHERE status=$1`, string(status)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count deliveries: %w", err)
	}
	return n, nil
}

const replayJobCols = `id, webhook_id, from_ts, to_ts, delivery_ids, status, events_replayed, error, created_at, updated_at`

// CreateJob inserts a replay job.
func (s *WebhookStore) CreateJob(ctx context.Context, j events.ReplayJob) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO webhook_replay_job
			(id, webhook_id, from_ts, to_ts, delivery_ids, status, events_replayed, error, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,0,'',now(),now())`,
		j.ID, j.WebhookID, nullTime(j.From), nullTime(j.To), j.DeliveryIDs, string(j.Status))
	if err != nil {
		return fmt.Errorf("create replay job: %w", err)
	}
	return nil
}

// GetJob returns a replay job by id.
func (s *WebhookStore) GetJob(ctx context.Context, id string) (events.ReplayJob, bool, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+replayJobCols+` FROM webhook_replay_job WHERE id=$1`, id)
	j, err := scanReplayJob(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return events.ReplayJob{}, false, nil
		}
		return events.ReplayJob{}, false, fmt.Errorf("get replay job: %w", err)
	}
	return j, true, nil
}

// ListJobs returns recent replay jobs (newest first).
func (s *WebhookStore) ListJobs(ctx context.Context, limit int) ([]events.ReplayJob, error) {
	return s.queryJobs(ctx, `SELECT `+replayJobCols+` FROM webhook_replay_job ORDER BY created_at DESC LIMIT $1`, limit)
}

// ClaimPendingJobs returns pending replay jobs to execute.
func (s *WebhookStore) ClaimPendingJobs(ctx context.Context, limit int) ([]events.ReplayJob, error) {
	return s.queryJobs(ctx, `SELECT `+replayJobCols+` FROM webhook_replay_job WHERE status='pending' ORDER BY created_at LIMIT $1`, limit)
}

func (s *WebhookStore) queryJobs(ctx context.Context, sql string, limit int) ([]events.ReplayJob, error) {
	rows, err := s.pool.Query(ctx, sql, limit)
	if err != nil {
		return nil, fmt.Errorf("list replay jobs: %w", err)
	}
	defer rows.Close()
	var out []events.ReplayJob
	for rows.Next() {
		j, err := scanReplayJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan replay job: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// UpdateJob persists a replay job's status/progress.
func (s *WebhookStore) UpdateJob(ctx context.Context, j events.ReplayJob) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE webhook_replay_job
		   SET status=$2, events_replayed=$3, error=$4, updated_at=now()
		 WHERE id=$1`,
		j.ID, string(j.Status), j.EventsReplayed, j.Error)
	if err != nil {
		return fmt.Errorf("update replay job: %w", err)
	}
	return nil
}

func scanReplayJob(sc rowScanner) (events.ReplayJob, error) {
	var j events.ReplayJob
	var status string
	var from, to *time.Time
	if err := sc.Scan(&j.ID, &j.WebhookID, &from, &to, &j.DeliveryIDs, &status,
		&j.EventsReplayed, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
		return events.ReplayJob{}, err
	}
	j.Status = events.ReplayJobStatus(status)
	if from != nil {
		j.From = *from
	}
	if to != nil {
		j.To = *to
	}
	return j, nil
}

func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
