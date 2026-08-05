package postgres

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const (
	defaultEscalationPolicyPageSize = 50
	maxEscalationPolicyPageSize     = 500
	defaultEscalationTierPageSize   = 50
	maxEscalationTierPageSize       = 500
)

const escalationPolicyCols = `policy_id, tenant_id, name, status, row_version, created_at, updated_at`

const escalationTierCols = `tier_id, tenant_id, policy_id, position, on_call_schedule_id, wait_seconds,
	row_version, created_at, updated_at`

// EscalationPolicyStore administers escalation_policy/escalation_tier
// (E5.2b-1).
//
// Both tables are TENANT-OWNED — they are in TenantOwnedTables and carry
// row-level security — so this store is built from the tenant-scoped pool
// and takes no tenant argument anywhere: the bound connection already is the
// boundary (ADR-TENANCY-002), exactly like OnCallScheduleStore. There is no
// privileged-pool second role here: this story is configuration only — the
// escalation ENGINE that consumes this configuration (E5.2b-2) is entirely
// separate and out of scope.
type EscalationPolicyStore struct {
	pool *pgxpool.Pool
}

// NewEscalationPolicyStore builds the store over the given pool.
func NewEscalationPolicyStore(pool *pgxpool.Pool) *EscalationPolicyStore {
	return &EscalationPolicyStore{pool: pool}
}

var _ domain.EscalationPolicyRepository = (*EscalationPolicyStore)(nil)

func clampEscalationPolicyPage(limit int) int {
	if limit <= 0 {
		return defaultEscalationPolicyPageSize
	}
	if limit > maxEscalationPolicyPageSize {
		return maxEscalationPolicyPageSize
	}
	return limit
}

func clampEscalationTierPage(limit int) int {
	if limit <= 0 {
		return defaultEscalationTierPageSize
	}
	if limit > maxEscalationTierPageSize {
		return maxEscalationTierPageSize
	}
	return limit
}

func scanEscalationPolicy(sc scanner) (*domain.EscalationPolicy, error) {
	var (
		p      domain.EscalationPolicy
		status string
	)
	if err := sc.Scan(&p.PolicyID, &p.TenantID, &p.Name, &status, &p.RowVersion, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Status = domain.EscalationPolicyStatus(status)
	return &p, nil
}

func scanEscalationTier(sc scanner) (*domain.EscalationTier, error) {
	var t domain.EscalationTier
	if err := sc.Scan(
		&t.TierID, &t.TenantID, &t.PolicyID, &t.Position, &t.OnCallScheduleID, &t.WaitSeconds,
		&t.RowVersion, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &t, nil
}

// Create inserts a policy.
func (s *EscalationPolicyStore) Create(ctx context.Context, p *domain.EscalationPolicy) (*domain.EscalationPolicy, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO escalation_policy (policy_id, tenant_id, name, status, row_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,1,now(),now())
		RETURNING `+escalationPolicyCols,
		p.PolicyID, p.TenantID, p.Name, string(p.Status))

	created, err := scanEscalationPolicy(row)
	if err != nil {
		return nil, fmt.Errorf("insert escalation policy: %w", err)
	}
	return created, nil
}

// Get returns a policy by identifier, or domain.ErrNotFound.
func (s *EscalationPolicyStore) Get(ctx context.Context, policyID string) (*domain.EscalationPolicy, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+escalationPolicyCols+` FROM escalation_policy WHERE policy_id = $1`, policyID)
	p, err := scanEscalationPolicy(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get escalation policy: %w", err)
	}
	return p, nil
}

// List returns a page of the caller's policies, keyset-paginated over
// policy_id.
func (s *EscalationPolicyStore) List(ctx context.Context, limit int, after string) ([]*domain.EscalationPolicy, error) {
	limit = clampEscalationPolicyPage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+escalationPolicyCols+`
		  FROM escalation_policy
		 WHERE ($1 = '' OR policy_id > $1)
		 ORDER BY policy_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list escalation policies: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.EscalationPolicy, 0, limit)
	for rows.Next() {
		p, err := scanEscalationPolicy(rows)
		if err != nil {
			return nil, fmt.Errorf("scan escalation policy: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate escalation policies: %w", err)
	}
	return out, nil
}

// Update changes one or more fields under optimistic locking.
func (s *EscalationPolicyStore) Update(
	ctx context.Context, policyID string, rowVersion int64, patch domain.EscalationPolicyPatch,
) (*domain.EscalationPolicy, error) {
	current, err := s.Get(ctx, policyID)
	if err != nil {
		return nil, err
	}
	if patch.Name != nil {
		current.Name = *patch.Name
	}
	if patch.Status != nil {
		current.Status = *patch.Status
	}
	if err := current.Validate(); err != nil {
		return nil, err
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE escalation_policy
		   SET name = $3, status = $4, row_version = row_version + 1, updated_at = now()
		 WHERE policy_id = $1 AND row_version = $2
		RETURNING `+escalationPolicyCols,
		policyID, rowVersion, current.Name, string(current.Status))

	updated, err := scanEscalationPolicy(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, policyID)
	}
	if err != nil {
		return nil, fmt.Errorf("update escalation policy: %w", err)
	}
	return updated, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row — mirrors OnCallScheduleStore.explainMissedUpdate.
func (s *EscalationPolicyStore) explainMissedUpdate(ctx context.Context, policyID string) error {
	if _, err := s.Get(ctx, policyID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}

// Delete removes a policy; ON DELETE CASCADE removes its tiers with it.
func (s *EscalationPolicyStore) Delete(ctx context.Context, policyID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM escalation_policy WHERE policy_id = $1`, policyID)
	if err != nil {
		return fmt.Errorf("delete escalation policy: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// verifyScheduleInTenant confirms onCallScheduleID names a schedule visible
// to the caller's tenant, on THIS store's own tenant-scoped, RLS-enforced
// connection.
//
// This is load-bearing, not defensive: on_call_schedule and escalation_tier
// are both tenant-owned tables, and PostgreSQL's foreign-key triggers run
// with the constraint's own privileges and BYPASS row-level security on the
// referenced table — a documented PostgreSQL behaviour, not a bug (see
// AssetStore.CreateRelationship's identical comment, ADR-ASSET-001 §6). Left
// to the `on_call_schedule_id REFERENCES on_call_schedule (schedule_id)`
// constraint alone, a caller could name another tenant's schedule_id and have
// the FK accept it, creating a tier that would page across a tenant
// boundary. This SELECT runs the same policy every other read on this
// connection does, so a cross-tenant id is indistinguishable from one that
// does not exist at all — both report ErrNotFound.
func (s *EscalationPolicyStore) verifyScheduleInTenant(ctx context.Context, tx pgx.Tx, onCallScheduleID string) error {
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM on_call_schedule WHERE schedule_id = $1)`, onCallScheduleID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check tier schedule exists: %w", err)
	}
	if !exists {
		return domain.ErrNotFound
	}
	return nil
}

// lockPolicyTx takes FOR UPDATE on policyID's own row, serialising concurrent
// AddTier/RemoveTier/ReorderTiers calls against the same policy — the same
// lock-the-owning-row technique OnCallScheduleStore.lockScheduleTx uses. It
// also confirms policyID exists, returning domain.ErrNotFound otherwise.
func (s *EscalationPolicyStore) lockPolicyTx(ctx context.Context, tx pgx.Tx, policyID string) error {
	var exists bool
	err := tx.QueryRow(ctx,
		`SELECT true FROM escalation_policy WHERE policy_id = $1 FOR UPDATE`, policyID,
	).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock escalation policy: %w", err)
	}
	return nil
}

// AddTier appends a tier pointed at onCallScheduleID to policyID's ladder at
// the next position.
func (s *EscalationPolicyStore) AddTier(
	ctx context.Context, policyID, onCallScheduleID string, waitSeconds int,
) (*domain.EscalationTier, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.lockPolicyTx(ctx, tx, policyID); err != nil {
		return nil, err
	}
	if err := s.verifyScheduleInTenant(ctx, tx, onCallScheduleID); err != nil {
		return nil, err
	}

	var nextPosition int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(position) + 1, 0) FROM escalation_tier WHERE policy_id = $1`, policyID,
	).Scan(&nextPosition); err != nil {
		return nil, fmt.Errorf("compute next escalation tier position: %w", err)
	}

	t, err := domain.NewEscalationTier(policyID, domain.TenantIDFrom(ctx), onCallScheduleID, waitSeconds, nextPosition)
	if err != nil {
		return nil, err
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO escalation_tier
			(tier_id, tenant_id, policy_id, position, on_call_schedule_id, wait_seconds, row_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,1,now(),now())
		RETURNING `+escalationTierCols,
		t.TierID, t.TenantID, t.PolicyID, t.Position, t.OnCallScheduleID, t.WaitSeconds)

	added, err := scanEscalationTier(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, domain.ErrConflict
		}
		if isForeignKeyViolation(err) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("insert escalation tier: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return added, nil
}

// RemoveTier removes one tier and closes the resulting gap in position,
// keeping positions contiguous 0..N-1.
func (s *EscalationPolicyStore) RemoveTier(ctx context.Context, policyID, tierID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.lockPolicyTx(ctx, tx, policyID); err != nil {
		return err
	}

	var removedPosition int
	err = tx.QueryRow(ctx,
		`DELETE FROM escalation_tier WHERE tier_id = $1 AND policy_id = $2 RETURNING position`,
		tierID, policyID,
	).Scan(&removedPosition)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("delete escalation tier: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE escalation_tier
		   SET position = position - 1, row_version = row_version + 1, updated_at = now()
		 WHERE policy_id = $1 AND position > $2`,
		policyID, removedPosition); err != nil {
		return fmt.Errorf("compact escalation tier positions: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ListTiers returns a page of policyID's ladder in escalation order,
// keyset-paginated over position itself.
func (s *EscalationPolicyStore) ListTiers(
	ctx context.Context, policyID string, limit int, after string,
) ([]*domain.EscalationTier, error) {
	limit = clampEscalationTierPage(limit)
	afterPosition := -1
	if after != "" {
		n, err := strconv.Atoi(after)
		if err != nil {
			return nil, domain.NewValidationError("after", "must be an integer position cursor")
		}
		afterPosition = n
	}

	rows, err := s.pool.Query(ctx, `
		SELECT `+escalationTierCols+`
		  FROM escalation_tier
		 WHERE policy_id = $1 AND position > $2
		 ORDER BY position
		 LIMIT $3`, policyID, afterPosition, limit)
	if err != nil {
		return nil, fmt.Errorf("list escalation tiers: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.EscalationTier, 0, limit)
	for rows.Next() {
		t, err := scanEscalationTier(rows)
		if err != nil {
			return nil, fmt.Errorf("scan escalation tier: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate escalation tiers: %w", err)
	}
	return out, nil
}

// ReorderTiers atomically replaces policyID's ladder order.
//
// tierIDs must be exactly the policy's current tier set, in the desired new
// order. The set-equality check happens inside the same transaction that
// holds the policy row lock (lockPolicyTx), so a concurrent Add/Remove cannot
// race between the check and the write.
//
// uq_escalation_tier_policy_position is DEFERRABLE INITIALLY DEFERRED
// (20260902000001), so the per-row UPDATEs below may transiently pass
// through a duplicate position (e.g. swapping positions 0 and 1) — PostgreSQL
// checks the constraint at COMMIT, once every row has its final,
// non-colliding position — the identical technique
// OnCallScheduleStore.ReorderParticipants already uses over
// uq_on_call_participant_schedule_position.
func (s *EscalationPolicyStore) ReorderTiers(
	ctx context.Context, policyID string, tierIDs []string,
) ([]*domain.EscalationTier, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.lockPolicyTx(ctx, tx, policyID); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `SELECT tier_id FROM escalation_tier WHERE policy_id = $1`, policyID)
	if err != nil {
		return nil, fmt.Errorf("read escalation tiers for reorder: %w", err)
	}
	current := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan escalation tier id: %w", err)
		}
		current[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate escalation tier ids: %w", err)
	}
	rows.Close()

	if len(tierIDs) != len(current) {
		return nil, domain.NewValidationError("tier_ids",
			"must name exactly the policy's current tiers, no more, no fewer")
	}
	seen := make(map[string]bool, len(tierIDs))
	for _, id := range tierIDs {
		if !current[id] {
			return nil, domain.NewValidationError("tier_ids", fmt.Sprintf("%q is not a tier of this policy", id))
		}
		if seen[id] {
			return nil, domain.NewValidationError("tier_ids", fmt.Sprintf("%q is repeated", id))
		}
		seen[id] = true
	}

	for i, id := range tierIDs {
		if _, err := tx.Exec(ctx, `
			UPDATE escalation_tier
			   SET position = $3, row_version = row_version + 1, updated_at = now()
			 WHERE tier_id = $1 AND policy_id = $2`,
			id, policyID, i); err != nil {
			return nil, fmt.Errorf("reorder escalation tier: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reorder: %w", err)
	}
	return s.ListTiers(ctx, policyID, len(tierIDs), "")
}
