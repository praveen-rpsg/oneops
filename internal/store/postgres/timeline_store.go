package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/timeline"
)

// TimelineStore is the READ-ONLY data source for the execution timeline (PRS-021).
// Every method is a SELECT over EXISTING tables (audit_event, webhook_delivery,
// webhook_replay_job, policy_execution). It writes nothing, computes no execution
// state, and modifies no subsystem — it only composes what others committed.
type TimelineStore struct {
	pool *pgxpool.Pool
}

// NewTimelineStore builds a read-only timeline source over the pool.
func NewTimelineStore(pool *pgxpool.Pool) *TimelineStore { return &TimelineStore{pool: pool} }

var _ timeline.Source = (*TimelineStore)(nil)

// AuditByEventID returns committed audit events with the given event id.
func (s *TimelineStore) AuditByEventID(ctx context.Context, eventID string) ([]timeline.AuditRow, error) {
	return s.auditRows(ctx,
		`SELECT chain_id, seq, event_id, operation_id, operation, actor, occurred_at
		   FROM audit_event WHERE event_id=$1 ORDER BY chain_id, seq`, eventID)
}

// AuditByChain returns committed audit events for a chain (governance object).
func (s *TimelineStore) AuditByChain(ctx context.Context, chainID string, limit int) ([]timeline.AuditRow, error) {
	return s.auditRows(ctx,
		`SELECT chain_id, seq, event_id, operation_id, operation, actor, occurred_at
		   FROM audit_event WHERE chain_id=$1 ORDER BY seq LIMIT $2`, chainID, limit)
}

func (s *TimelineStore) auditRows(ctx context.Context, sql string, args ...any) ([]timeline.AuditRow, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("timeline audit query: %w", err)
	}
	defer rows.Close()
	var out []timeline.AuditRow
	for rows.Next() {
		var a timeline.AuditRow
		if err := rows.Scan(&a.ChainID, &a.Seq, &a.EventID, &a.OperationID, &a.Operation, &a.Actor, &a.OccurredAt); err != nil {
			return nil, fmt.Errorf("timeline scan audit: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeliveriesByEventIDs returns webhook deliveries for the given event ids.
func (s *TimelineStore) DeliveriesByEventIDs(ctx context.Context, eventIDs []string) ([]timeline.DeliveryRow, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, webhook_id, event_id, status, last_status_code, last_attempt, created_at
		  FROM webhook_delivery WHERE event_id = ANY($1) ORDER BY created_at`, eventIDs)
	if err != nil {
		return nil, fmt.Errorf("timeline delivery query: %w", err)
	}
	defer rows.Close()
	var out []timeline.DeliveryRow
	for rows.Next() {
		var d timeline.DeliveryRow
		var last *time.Time
		if err := rows.Scan(&d.ID, &d.WebhookID, &d.EventID, &d.Status, &d.StatusCode, &last, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("timeline scan delivery: %w", err)
		}
		if last != nil {
			d.LastAttempt = *last
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PolicyExecutionsByEventIDs returns policy executions for the given event ids.
func (s *TimelineStore) PolicyExecutionsByEventIDs(ctx context.Context, eventIDs []string) ([]timeline.PolicyRow, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, policy_id, event_id, status, retry_count, started_at, ended_at, created_at
		  FROM policy_execution WHERE event_id = ANY($1) ORDER BY created_at`, eventIDs)
	if err != nil {
		return nil, fmt.Errorf("timeline policy query: %w", err)
	}
	defer rows.Close()
	return scanPolicyRows(rows)
}

// ReplayJob returns a replay job record.
func (s *TimelineStore) ReplayJob(ctx context.Context, jobID string) (timeline.ReplayRow, bool, error) {
	var r timeline.ReplayRow
	err := s.pool.QueryRow(ctx, `
		SELECT id, webhook_id, status, events_replayed, created_at, updated_at
		  FROM webhook_replay_job WHERE id=$1`, jobID).
		Scan(&r.ID, &r.WebhookID, &r.Status, &r.EventsReplayed, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return timeline.ReplayRow{}, false, nil
	}
	if err != nil {
		return timeline.ReplayRow{}, false, fmt.Errorf("timeline replay query: %w", err)
	}
	return r, true, nil
}

// PolicyExecution returns a single policy execution record.
func (s *TimelineStore) PolicyExecution(ctx context.Context, execID string) (timeline.PolicyRow, bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, policy_id, event_id, status, retry_count, started_at, ended_at, created_at
		  FROM policy_execution WHERE id=$1`, execID)
	if err != nil {
		return timeline.PolicyRow{}, false, fmt.Errorf("timeline policy get: %w", err)
	}
	defer rows.Close()
	ps, err := scanPolicyRows(rows)
	if err != nil {
		return timeline.PolicyRow{}, false, err
	}
	if len(ps) == 0 {
		return timeline.PolicyRow{}, false, nil
	}
	return ps[0], true, nil
}

func scanPolicyRows(rows pgx.Rows) ([]timeline.PolicyRow, error) {
	var out []timeline.PolicyRow
	for rows.Next() {
		var p timeline.PolicyRow
		var started, ended *time.Time
		if err := rows.Scan(&p.ID, &p.PolicyID, &p.EventID, &p.Status, &p.RetryCount, &started, &ended, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("timeline scan policy: %w", err)
		}
		if started != nil {
			p.StartedAt = *started
		}
		if ended != nil {
			p.EndedAt = *ended
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
