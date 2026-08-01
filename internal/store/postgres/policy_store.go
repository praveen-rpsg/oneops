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

// tenant_id is read on every path so the consumer's fan-out can compare
// ownership: it lists policies across all tenants on a privileged connection.
const policyCols = `id, name, enabled, condition, action_type, action_config, steps, max_retries, created_at, updated_at, tenant_id`

// Create inserts a policy.
func (s *PolicyStore) Create(ctx context.Context, p policy.Policy) error {
	cond, err := json.Marshal(p.Condition)
	if err != nil {
		return fmt.Errorf("marshal condition: %w", err)
	}
	steps, err := sequenceJSON(p.Steps)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO policy (`+policyCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now(),now(),$9)`,
		p.ID, p.Name, p.Enabled, cond, p.Action.Type, actionConfig(p.Action.Config), steps, p.MaxRetries,
		domain.TenantIDFrom(ctx))
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
	steps, err := sequenceJSON(p.Steps)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE policy
		   SET name=$2, enabled=$3, condition=$4, action_type=$5, action_config=$6,
		       steps=$7, max_retries=$8, updated_at=now()
		 WHERE id=$1`,
		p.ID, p.Name, p.Enabled, cond, p.Action.Type, actionConfig(p.Action.Config), steps, p.MaxRetries)
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

const execCols = `id, policy_id, event_id, event, status, retry_count, error, started_at, ended_at, next_attempt_at, created_at, claimed_at, run_id, step_index, gate`

// Enqueue inserts pending executions (idempotent by id). RunID/StepIndex/Gate
// default to their zero values for a single-action policy's execution,
// exactly as every execution behaved before those columns existed; a
// composed Sequence run's steps set them explicitly (ADR-POLICY-001).
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
				(id, policy_id, event_id, event, status, retry_count, next_attempt_at, created_at,
				 run_id, step_index, gate)
			VALUES ($1,$2,$3,$4,$5,0,$6,now(),$7,$8,$9)
			ON CONFLICT (id) DO NOTHING`,
			x.ID, x.PolicyID, x.Event.EventID, ev, string(x.Status), x.NextAttemptAt,
			nullString(x.RunID), x.StepIndex, string(x.Gate))
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
//
// As on the delivery queue, the claim enforces the retry budget
// (ADR-CONCURRENCY-006): retry_count is "attempts started" and is incremented as
// the row is handed out, so an execution whose worker never reports back still
// consumes budget, and one whose next attempt would exceed the policy's
// max_retries is dead-lettered in the same statement instead of being run again.
// A missing policy is budget 0. Without this, a crash-looping action was re-run
// forever with no terminating state (proven live).
func (s *PolicyStore) ClaimDue(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]policy.Execution, error) {
	// Atomic claim with lease, matching the delivery queue (ADR-CONCURRENCY-002):
	// due pending/failed rows and stale running rows are moved to 'running' under
	// FOR UPDATE SKIP LOCKED, so two workers never run the same execution and a
	// crashed worker's row is recovered only after the lease.
	staleBefore := now.Add(-lease)
	rows, err := s.pool.Query(ctx, `
		WITH candidate AS (
		    SELECT e.id,
		           e.retry_count + 1 AS attempt_no,
		           COALESCE(p.max_retries, 0) AS budget
		      FROM policy_execution e
		      LEFT JOIN policy p ON p.id = e.policy_id
		     WHERE (e.status IN ('pending','failed') AND e.next_attempt_at <= $1)
		        OR (e.status = 'running' AND e.claimed_at < $2)
		     ORDER BY e.next_attempt_at
		     LIMIT $3
		     FOR UPDATE OF e SKIP LOCKED
		),
		exhausted AS (
		    UPDATE policy_execution e
		       SET status = 'dead_letter', claimed_at = NULL
		      FROM candidate c
		     WHERE e.id = c.id AND c.attempt_no > c.budget
		),
		claimed AS (
		    UPDATE policy_execution e
		       SET status = 'running', claimed_at = $1, retry_count = c.attempt_no
		      FROM candidate c
		     WHERE e.id = c.id AND c.attempt_no <= c.budget
		    RETURNING `+prefixCols(execCols, "e")+`
		)
		SELECT `+execCols+` FROM claimed`, now, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("claim executions: %w", err)
	}
	defer rows.Close()
	return scanExecutions(rows)
}

// ReleaseClaim returns an unused claim to the queue, giving back the attempt it
// consumed. See WebhookStore.ReleaseClaim — a worker stopped between claiming
// and running must not burn budget it never spent. Fenced on the claim token.
func (s *PolicyStore) ReleaseClaim(ctx context.Context, id string, claimToken time.Time) error {
	if claimToken.IsZero() {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE policy_execution
		   SET status = 'pending', claimed_at = NULL,
		       retry_count = GREATEST(retry_count - 1, 0)
		 WHERE id = $1 AND claimed_at = $2 AND status = 'running'`, id, claimToken)
	if err != nil {
		return fmt.Errorf("release execution claim: %w", err)
	}
	return nil
}

// MarkResult records an execution attempt outcome, fenced by the claim token
// (ADR-CONCURRENCY-005). The write lands only if the row is still claimed under
// `claimToken`; a worker whose lease expired and whose row was reclaimed holds a
// stale token, matches zero rows, and gets policy.ErrStaleClaim — so its late
// completion cannot overwrite the reclaimer's outcome or re-run the action's
// bookkeeping. A zero token (the admin test path) writes unfenced.
func (s *PolicyStore) MarkResult(ctx context.Context, id string, claimToken time.Time, status policy.ExecutionStatus, retry int, errMsg string, started, ended, next time.Time) error {
	var tokenPtr *time.Time
	if !claimToken.IsZero() {
		tokenPtr = &claimToken
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE policy_execution
		   SET status=$2, retry_count=$3, error=$4, started_at=$5, ended_at=$6,
		       next_attempt_at=COALESCE($7, next_attempt_at)
		 WHERE id=$1 AND ($8::timestamptz IS NULL OR claimed_at = $8)`,
		id, string(status), retry, errMsg, nullTime(started), nullTime(ended), nullTime(next), tokenPtr)
	if err != nil {
		return fmt.Errorf("mark execution: %w", err)
	}
	if tag.RowsAffected() == 0 && tokenPtr != nil {
		return policy.ErrStaleClaim
	}
	return nil
}

// SetGate records a step's Decision gate resolution (ADR-POLICY-001 §3).
// Unfenced by a claim token: it runs after the step's own outcome is already
// durably recorded (MarkResult succeeded), so there is no in-flight claim left
// to fence against — it is a plain write to the gate column, not a status
// transition.
func (s *PolicyStore) SetGate(ctx context.Context, id string, gate policy.GateState) error {
	tag, err := s.pool.Exec(ctx, `UPDATE policy_execution SET gate=$2 WHERE id=$1`, id, string(gate))
	if err != nil {
		return fmt.Errorf("set execution gate: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
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

// SetPolicyCursor advances the consumer cursor for a chain. The write is
// monotonic (GREATEST): the watermark never moves backward, so a stale or
// overlapping consumer cannot rewind it under a concurrent advance
// (ADR-CONCURRENCY-004). A cursor never legitimately regresses — the consumer only
// advances to the max seq of events it has already enqueued.
func (s *PolicyStore) SetPolicyCursor(ctx context.Context, chainID string, seq int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO policy_cursor (chain_id, last_seq, updated_at)
		VALUES ($1,$2,now())
		ON CONFLICT (chain_id) DO UPDATE
		   SET last_seq=GREATEST(policy_cursor.last_seq, EXCLUDED.last_seq), updated_at=now()`,
		chainID, seq)
	if err != nil {
		return fmt.Errorf("set policy cursor: %w", err)
	}
	return nil
}

func scanPolicy(sc rowScanner) (policy.Policy, error) {
	var p policy.Policy
	var cond, cfg, steps []byte
	if err := sc.Scan(&p.ID, &p.Name, &p.Enabled, &cond, &p.Action.Type, &cfg, &steps, &p.MaxRetries,
		&p.CreatedAt, &p.UpdatedAt, &p.TenantID); err != nil {
		return policy.Policy{}, err
	}
	if len(cond) > 0 {
		if err := json.Unmarshal(cond, &p.Condition); err != nil {
			return policy.Policy{}, fmt.Errorf("unmarshal condition: %w", err)
		}
	}
	p.Action.Config = json.RawMessage(cfg)
	if len(steps) > 0 {
		if err := json.Unmarshal(steps, &p.Steps); err != nil {
			return policy.Policy{}, fmt.Errorf("unmarshal steps: %w", err)
		}
	}
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
	var status, gate string
	var ev []byte
	var started, ended, claimedAt *time.Time
	var runID *string
	if err := sc.Scan(&x.ID, &x.PolicyID, new(string), &ev, &status, &x.RetryCount, &x.Error,
		&started, &ended, &x.NextAttemptAt, &x.CreatedAt, &claimedAt, &runID, &x.StepIndex, &gate); err != nil {
		return policy.Execution{}, err
	}
	x.Status = policy.ExecutionStatus(status)
	x.Gate = policy.GateState(gate)
	if runID != nil {
		x.RunID = *runID
	}
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
	if claimedAt != nil {
		x.ClaimedAt = *claimedAt
	}
	return x, nil
}

func actionConfig(c json.RawMessage) []byte {
	if len(c) == 0 {
		return []byte("{}")
	}
	return c
}

// sequenceJSON marshals a composed Sequence, defaulting to an empty JSON
// array so a Policy with no Steps (the overwhelming majority, today all of
// them) stores the same "[]" a fresh policy table row already defaults to.
func sequenceJSON(seq policy.Sequence) ([]byte, error) {
	if len(seq) == 0 {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(seq)
	if err != nil {
		return nil, fmt.Errorf("marshal steps: %w", err)
	}
	return b, nil
}

// nullString maps an empty Go string to SQL NULL, so an unused nullable
// column (run_id, for an execution outside any composed run) stores NULL
// rather than an empty string two different unrelated rows could collide on.
func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ListByRun returns every Execution sharing runID, ordered by StepIndex — the
// persistence side of policy.RunProgress (ADR-POLICY-001).
func (s *PolicyStore) ListByRun(ctx context.Context, runID string) ([]policy.Execution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+execCols+` FROM policy_execution
		 WHERE run_id=$1 ORDER BY step_index`, runID)
	if err != nil {
		return nil, fmt.Errorf("list run executions: %w", err)
	}
	defer rows.Close()
	return scanExecutions(rows)
}

// ListStalledRuns finds composed runs permanently wedged by the gap
// policy.Reconciler exists to close: a frontier Execution durably succeeded
// with a resolved gate (none, or already passed) but its immediate
// successor step (step_index+1) was never enqueued, because the worker that
// recorded the success crashed before calling Enqueue. ClaimDue never
// reselects a succeeded row, so nothing else re-discovers this.
//
// The NOT EXISTS successor lookup is an equality match on run_id, served by
// ix_policy_execution_run; step_index is then filtered over the handful of rows
// one run ever has. The outer candidate relation, however, is scanned and
// filtered (run_id IS NOT NULL AND status = succeeded AND gate is none or passed)
// — no index covers that composite predicate, so on a table dominated by
// single-action rows the planner may seq-scan it. Acceptable and no new index
// needed because the sweep is periodic (~30s) and LIMIT-bounded; if the
// execution table grows large enough that this scan bites, add a partial index
// on (status) WHERE run_id IS NOT NULL.
//
// This over-includes a run whose frontier is genuinely its LAST step (there
// is no step_index+1 because the run is complete, not because one is
// missing): that is intentional and harmless. The caller re-derives the
// run's real cursor via policy.RunProgress before acting, and a run
// RunProgress already reports Complete is always a no-op there — filtering
// "is this run actually complete" here would require re-deriving the
// Policy's Sequence length in SQL, duplicating logic RunProgress already
// owns.
func (s *PolicyStore) ListStalledRuns(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT e.run_id
		  FROM policy_execution e
		 WHERE e.run_id IS NOT NULL
		   AND e.status = 'succeeded'
		   AND e.gate IN ('', 'passed')
		   AND NOT EXISTS (
		         SELECT 1 FROM policy_execution nx
		          WHERE nx.run_id = e.run_id AND nx.step_index = e.step_index + 1
		       )
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list stalled runs: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return nil, fmt.Errorf("scan stalled run: %w", err)
		}
		out = append(out, runID)
	}
	return out, rows.Err()
}

// ListPendingGates returns the Executions currently paused at an unresolved
// Decision gate (ADR-POLICY-002) — a succeeded step whose gate column is
// 'pending' — ordered by created_at so a test (and an operator) sees a
// deterministic sequence when several runs are paused at once. Unindexed
// beyond ix_policy_execution's existing (status) shape for the same reason
// ListStalledRuns' composite predicate is: paused-gate rows are a small
// fraction of the table and this sweep is periodic and LIMIT-bounded.
func (s *PolicyStore) ListPendingGates(ctx context.Context, limit int) ([]policy.Execution, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+execCols+` FROM policy_execution
		 WHERE run_id IS NOT NULL AND status = 'succeeded' AND gate = 'pending'
		 ORDER BY created_at
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list pending gates: %w", err)
	}
	defer rows.Close()
	return scanExecutions(rows)
}
