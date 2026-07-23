package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/policy"
)

// PolicyStore persists the policy registry, execution history, and consumer
// cursor (PRS-020). It is additive infrastructure — it never reads or writes the
// governance or audit tables. It satisfies the policy package's persistence ports.
type PolicyStore struct {
	pool *pgxpool.Pool
}

// NewPolicyStore builds a store over the pool.
func NewPolicyStore(pool *pgxpool.Pool) *PolicyStore { return &PolicyStore{pool: pool} }

var (
	_ policy.Store          = (*PolicyStore)(nil)
	_ policy.ExecutionStore = (*PolicyStore)(nil)
	_ policy.CursorStore    = (*PolicyStore)(nil)
)

const policyCols = `id, name, enabled, condition, action_type, action_config, max_retries, created_at, updated_at`

// Create inserts a policy.
func (s *PolicyStore) Create(ctx context.Context, p policy.Policy) error {
	cond, err := json.Marshal(p.Condition)
	if err != nil {
		return fmt.Errorf("marshal condition: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO policy (`+policyCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,now(),now())`,
		p.ID, p.Name, p.Enabled, cond, p.Action.Type, actionConfig(p.Action.Config), p.MaxRetries)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ErrConflict
		}
		return fmt.Errorf("create policy: %w", err)
	}
	return nil
}

// Get returns a policy or domain.ErrNotFound.
func (s *PolicyStore) Get(ctx context.Context, id string) (policy.Policy, error) {
	p, err := scanPolicy(s.pool.QueryRow(ctx, `SELECT `+policyCols+` FROM policy WHERE id=$1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return policy.Policy{}, domain.ErrNotFound
	}
	if err != nil {
		return policy.Policy{}, fmt.Errorf("get policy: %w", err)
	}
	return p, nil
}

// List returns all policies.
func (s *PolicyStore) List(ctx context.Context) ([]policy.Policy, error) {
	return s.queryPolicies(ctx, `SELECT `+policyCols+` FROM policy ORDER BY created_at`)
}

// ListEnabled returns only enabled policies.
func (s *PolicyStore) ListEnabled(ctx context.Context) ([]policy.Policy, error) {
	return s.queryPolicies(ctx, `SELECT `+policyCols+` FROM policy WHERE enabled ORDER BY created_at`)
}

func (s *PolicyStore) queryPolicies(ctx context.Context, sql string) ([]policy.Policy, error) {
	rows, err := s.pool.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("list policies: %w", err)
	}
	defer rows.Close()
	var out []policy.Policy
	for rows.Next() {
		p, err := scanPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("scan policy: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Update replaces a policy's mutable fields.
func (s *PolicyStore) Update(ctx context.Context, p policy.Policy) error {
	cond, err := json.Marshal(p.Condition)
	if err != nil {
		return fmt.Errorf("marshal condition: %w", err)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE policy
		   SET name=$2, enabled=$3, condition=$4, action_type=$5, action_config=$6,
		       max_retries=$7, updated_at=now()
		 WHERE id=$1`,
		p.ID, p.Name, p.Enabled, cond, p.Action.Type, actionConfig(p.Action.Config), p.MaxRetries)
	if err != nil {
		return fmt.Errorf("update policy: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Delete removes a policy (its execution history is retained).
func (s *PolicyStore) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM policy WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("delete policy: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

const execCols = `id, policy_id, event_id, event, status, retry_count, error, started_at, ended_at, next_attempt_at, created_at`

// Enqueue inserts pending executions (idempotent by id).
func (s *PolicyStore) Enqueue(ctx context.Context, xs []policy.Execution) error {
	batch := &pgx.Batch{}
	for i := range xs {
		x := xs[i]
		ev, err := json.Marshal(x.Event)
		if err != nil {
			return fmt.Errorf("marshal event: %w", err)
		}
		batch.Queue(`
			INSERT INTO policy_execution
				(id, policy_id, event_id, event, status, retry_count, next_attempt_at, created_at)
			VALUES ($1,$2,$3,$4,$5,0,$6,now())
			ON CONFLICT (id) DO NOTHING`,
			x.ID, x.PolicyID, x.Event.EventID, ev, string(x.Status), x.NextAttemptAt)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range xs {
		if _, err := br.Exec(); err != nil {
			return fmt.Errorf("enqueue execution: %w", err)
		}
	}
	return nil
}

// ClaimDue returns executions eligible to run (pending/failed and due).
func (s *PolicyStore) ClaimDue(ctx context.Context, now time.Time, limit int) ([]policy.Execution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+execCols+` FROM policy_execution
		 WHERE status IN ('pending','failed') AND next_attempt_at <= $1
		 ORDER BY next_attempt_at LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claim executions: %w", err)
	}
	defer rows.Close()
	return scanExecutions(rows)
}

// MarkResult records an execution attempt outcome.
func (s *PolicyStore) MarkResult(ctx context.Context, id string, status policy.ExecutionStatus, retry int, errMsg string, started, ended, next time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE policy_execution
		   SET status=$2, retry_count=$3, error=$4, started_at=$5, ended_at=$6,
		       next_attempt_at=COALESCE($7, next_attempt_at)
		 WHERE id=$1`,
		id, string(status), retry, errMsg, nullTime(started), nullTime(ended), nullTime(next))
	if err != nil {
		return fmt.Errorf("mark execution: %w", err)
	}
	return nil
}

// ListByPolicy returns a policy's recent executions (newest first).
func (s *PolicyStore) ListByPolicy(ctx context.Context, policyID string, limit int) ([]policy.Execution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+execCols+` FROM policy_execution
		 WHERE policy_id=$1 ORDER BY created_at DESC LIMIT $2`, policyID, limit)
	if err != nil {
		return nil, fmt.Errorf("list executions: %w", err)
	}
	defer rows.Close()
	return scanExecutions(rows)
}

// GetPolicyCursor returns the consumer cursor for a chain (0 when absent).
func (s *PolicyStore) GetPolicyCursor(ctx context.Context, chainID string) (int64, error) {
	var seq int64
	err := s.pool.QueryRow(ctx, `SELECT last_seq FROM policy_cursor WHERE chain_id=$1`, chainID).Scan(&seq)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("get policy cursor: %w", err)
	}
	return seq, nil
}

// SetPolicyCursor advances the consumer cursor for a chain.
func (s *PolicyStore) SetPolicyCursor(ctx context.Context, chainID string, seq int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO policy_cursor (chain_id, last_seq, updated_at)
		VALUES ($1,$2,now())
		ON CONFLICT (chain_id) DO UPDATE SET last_seq=EXCLUDED.last_seq, updated_at=now()`,
		chainID, seq)
	if err != nil {
		return fmt.Errorf("set policy cursor: %w", err)
	}
	return nil
}

func scanPolicy(sc rowScanner) (policy.Policy, error) {
	var p policy.Policy
	var cond, cfg []byte
	if err := sc.Scan(&p.ID, &p.Name, &p.Enabled, &cond, &p.Action.Type, &cfg, &p.MaxRetries, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return policy.Policy{}, err
	}
	if len(cond) > 0 {
		if err := json.Unmarshal(cond, &p.Condition); err != nil {
			return policy.Policy{}, fmt.Errorf("unmarshal condition: %w", err)
		}
	}
	p.Action.Config = json.RawMessage(cfg)
	return p, nil
}

func scanExecutions(rows pgx.Rows) ([]policy.Execution, error) {
	var out []policy.Execution
	for rows.Next() {
		x, err := scanExecution(rows)
		if err != nil {
			return nil, fmt.Errorf("scan execution: %w", err)
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func scanExecution(sc rowScanner) (policy.Execution, error) {
	var x policy.Execution
	var status string
	var ev []byte
	var started, ended *time.Time
	if err := sc.Scan(&x.ID, &x.PolicyID, new(string), &ev, &status, &x.RetryCount, &x.Error,
		&started, &ended, &x.NextAttemptAt, &x.CreatedAt); err != nil {
		return policy.Execution{}, err
	}
	x.Status = policy.ExecutionStatus(status)
	if len(ev) > 0 {
		if err := json.Unmarshal(ev, &x.Event); err != nil {
			return policy.Execution{}, fmt.Errorf("unmarshal event: %w", err)
		}
	}
	if started != nil {
		x.StartedAt = *started
	}
	if ended != nil {
		x.EndedAt = *ended
	}
	return x, nil
}

func actionConfig(c json.RawMessage) []byte {
	if len(c) == 0 {
		return []byte("{}")
	}
	return c
}
