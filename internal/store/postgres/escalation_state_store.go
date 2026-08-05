package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/escalation"
)

const escalationStateCols = `state_id, tenant_id, incident_id, policy_id, current_tier_index,
	next_attempt_at, claimed_at, status, attempts, row_version, created_at, updated_at`

// escalationSeedCandidatesQuery finds every open, unacknowledged,
// alert-sourced incident with no escalation_state row yet, across every
// tenant, paired with its tenant's default escalation policy (ADR-ONCALL-003
// §2, MVP): the tenant's own single active escalation_policy, or, when a
// tenant has declared more than one, the earliest by created_at (ties broken
// by policy_id for a total order) — per-asset/per-service policy ROUTING is
// explicitly deferred, so every incident in a tenant escalates through the
// same one ladder for now. policy_id is NULL when the tenant has no active
// policy at all; Seed leaves that candidate unseeded rather than treating it
// as an error.
//
// The LATERAL join's own ON ep.tenant_id = i.tenant_id is what keeps
// policy_id same-tenant as incident_id — this is the ONLY place that
// association is ever made, so escalation_state's two foreign keys can never
// straddle a tenant boundary even though nothing re-verifies them a second
// time at INSERT (see this migration's own header comment for why no
// second check is needed here, unlike EscalationPolicyStore.AddTier's
// re-verification of a client-supplied on_call_schedule_id).
const escalationSeedCandidatesQuery = `
	SELECT i.incident_id, i.tenant_id, p.policy_id
	  FROM incident i
	  LEFT JOIN LATERAL (
	        SELECT ep.policy_id
	          FROM escalation_policy ep
	         WHERE ep.tenant_id = i.tenant_id AND ep.status = 'active'
	         ORDER BY ep.created_at ASC, ep.policy_id ASC
	         LIMIT 1
	  ) p ON true
	 WHERE i.source = 'alert' AND i.status = 'open'
	   AND NOT EXISTS (SELECT 1 FROM escalation_state es WHERE es.incident_id = i.incident_id)
	 ORDER BY i.incident_id`

// EscalationStateStore administers escalation_state, the per-incident
// escalation work-queue (E5.2b-2, ADR-ONCALL-003). Built over the PRIVILEGED
// pool: the Seeder and Worker that use it both process every tenant from one
// process, the identical dual-role split notification/grouping/
// collector-check already draw in this codebase. This store is
// escalation_state's only WRITER, and there is still no HTTP write path or
// admin CRUD API for it at all.
//
// E7.1 (NOC overview) adds this type's first tenant-scoped READ role:
// CountActive is called through a SECOND instance built over the
// tenant-scoped appPool, not the privileged instance above — the identical
// dual-role split AlertRuleStore/IncidentStore already draw between their
// admin-CRUD and privileged-worker instances, applied here between a
// read-only projection and the privileged Seeder/Worker instead of between
// two writers.
type EscalationStateStore struct {
	pool *pgxpool.Pool
}

// NewEscalationStateStore builds the store over the given PRIVILEGED pool.
func NewEscalationStateStore(pool *pgxpool.Pool) *EscalationStateStore {
	return &EscalationStateStore{pool: pool}
}

var _ escalation.Store = (*EscalationStateStore)(nil)

func scanEscalationState(sc scanner) (*domain.EscalationState, error) {
	var (
		st        domain.EscalationState
		status    string
		claimedAt *time.Time
	)
	if err := sc.Scan(
		&st.StateID, &st.TenantID, &st.IncidentID, &st.PolicyID, &st.CurrentTierIndex,
		&st.NextAttemptAt, &claimedAt, &status, &st.Attempts, &st.RowVersion, &st.CreatedAt, &st.UpdatedAt,
	); err != nil {
		return nil, err
	}
	st.Status = domain.EscalationStateStatus(status)
	if claimedAt != nil {
		st.ClaimedAt = *claimedAt
	}
	return &st, nil
}

// Seed implements escalation.Store.
func (s *EscalationStateStore) Seed(ctx context.Context, now time.Time) (seeded, skippedNoPolicy int, err error) {
	rows, err := s.pool.Query(ctx, escalationSeedCandidatesQuery)
	if err != nil {
		return 0, 0, fmt.Errorf("list escalation seed candidates: %w", err)
	}
	type candidate struct {
		incidentID, tenantID string
		policyID             *string
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if scanErr := rows.Scan(&c.incidentID, &c.tenantID, &c.policyID); scanErr != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("scan escalation seed candidate: %w", scanErr)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, 0, fmt.Errorf("iterate escalation seed candidates: %w", err)
	}
	rows.Close()

	for _, c := range candidates {
		if c.policyID == nil {
			skippedNoPolicy++
			continue
		}
		st, buildErr := domain.NewEscalationState(c.tenantID, c.incidentID, *c.policyID)
		if buildErr != nil {
			return seeded, skippedNoPolicy, fmt.Errorf("build escalation state: %w", buildErr)
		}
		tag, execErr := s.pool.Exec(ctx, `
			INSERT INTO escalation_state
				(state_id, tenant_id, incident_id, policy_id, current_tier_index, next_attempt_at,
				 status, attempts, row_version, created_at, updated_at)
			VALUES ($1,$2,$3,$4,0,$5,'active',0,1,$5,$5)
			ON CONFLICT (tenant_id, incident_id) DO NOTHING`,
			st.StateID, st.TenantID, st.IncidentID, st.PolicyID, now)
		if execErr != nil {
			return seeded, skippedNoPolicy, fmt.Errorf("seed escalation state: %w", execErr)
		}
		if tag.RowsAffected() > 0 {
			seeded++
		}
	}
	return seeded, skippedNoPolicy, nil
}

// ClaimDue implements escalation.Store (ADR-CONCURRENCY-002/006/007),
// mirroring NotificationStore.ClaimDue's identical shape: claimed_at is the
// sole fencing token (status never changes on claim), FOR UPDATE SKIP LOCKED
// keeps two workers from claiming the same row, and the attempt is charged
// in the same statement that grants the claim.
func (s *EscalationStateStore) ClaimDue(
	ctx context.Context, now time.Time, lease time.Duration, limit int,
) ([]*domain.EscalationState, error) {
	staleBefore := now.Add(-lease)
	rows, err := s.pool.Query(ctx, `
		WITH candidate AS (
		    SELECT state_id
		      FROM escalation_state
		     WHERE status = 'active'
		       AND next_attempt_at <= $1
		       AND (claimed_at IS NULL OR claimed_at < $2)
		     ORDER BY next_attempt_at
		     LIMIT $3
		     FOR UPDATE SKIP LOCKED
		),
		claimed AS (
		    UPDATE escalation_state es
		       SET claimed_at = $1, attempts = es.attempts + 1, updated_at = now()
		      FROM candidate c
		     WHERE es.state_id = c.state_id
		    RETURNING `+prefixCols(escalationStateCols, "es")+`
		)
		SELECT `+escalationStateCols+` FROM claimed`, now, staleBefore, limit)
	if err != nil {
		return nil, fmt.Errorf("claim escalation states: %w", err)
	}
	defer rows.Close()

	var out []*domain.EscalationState
	for rows.Next() {
		st, err := scanEscalationState(rows)
		if err != nil {
			return nil, fmt.Errorf("scan escalation state: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// Advance implements escalation.Store, fenced on claimToken
// (ADR-CONCURRENCY-005).
func (s *EscalationStateStore) Advance(
	ctx context.Context, stateID string, claimToken time.Time, nextTierIndex int, nextAttemptAt time.Time,
) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE escalation_state
		   SET current_tier_index = $3, next_attempt_at = $4, claimed_at = NULL,
		       row_version = row_version + 1, updated_at = now()
		 WHERE state_id = $1 AND claimed_at = $2`,
		stateID, claimToken, nextTierIndex, nextAttemptAt)
	if err != nil {
		return fmt.Errorf("advance escalation state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return escalation.ErrStaleClaim
	}
	return nil
}

// MarkTerminal implements escalation.Store, fenced on claimToken
// (ADR-CONCURRENCY-005). claimed_at is cleared: every terminal status is
// final, so a stale claim left in place would serve no purpose.
func (s *EscalationStateStore) MarkTerminal(
	ctx context.Context, stateID string, claimToken time.Time, status domain.EscalationStateStatus,
) error {
	if !status.Terminal() {
		return fmt.Errorf("mark escalation state terminal: %q is not a terminal status", status)
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE escalation_state
		   SET status = $3, claimed_at = NULL, row_version = row_version + 1, updated_at = now()
		 WHERE state_id = $1 AND claimed_at = $2`,
		stateID, claimToken, string(status))
	if err != nil {
		return fmt.Errorf("mark escalation state terminal: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return escalation.ErrStaleClaim
	}
	return nil
}

// CountActive returns how many escalation_state rows are currently 'active'
// (E7.1's NOC overview projection) — a plain COUNT with no explicit
// tenant_id predicate, correct here ONLY because this method must be called
// through an instance built over the tenant-scoped appPool (see this type's
// own doc comment), where row-level security supplies the filter, exactly
// as every other tenant-scoped store in this package assumes. Bounded by
// ix_escalation_state_claim: that partial index's own condition
// (`WHERE status = 'active'`) matches this query's WHERE clause exactly, so
// Postgres can satisfy it by walking the index rather than the whole table —
// cost tracks the current work-queue depth, not escalation_state's full
// history.
func (s *EscalationStateStore) CountActive(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM escalation_state WHERE status = 'active'`,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active escalation states: %w", err)
	}
	return n, nil
}

// ReleaseClaim implements escalation.Store: claimed_at is cleared and the
// attempt it charged is given back, mirroring
// NotificationStore.ReleaseClaim exactly.
func (s *EscalationStateStore) ReleaseClaim(ctx context.Context, stateID string, claimToken time.Time) error {
	if claimToken.IsZero() {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE escalation_state
		   SET claimed_at = NULL, attempts = GREATEST(attempts - 1, 0), updated_at = now()
		 WHERE state_id = $1 AND claimed_at = $2`, stateID, claimToken)
	if err != nil {
		return fmt.Errorf("release escalation state claim: %w", err)
	}
	return nil
}
