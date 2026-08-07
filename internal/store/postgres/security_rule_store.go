package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const (
	defaultSecurityRulePageSize = 50
	maxSecurityRulePageSize     = 500
)

const securityRuleCols = `rule_id, tenant_id, asset_id, observation_type, min_severity, window_seconds,
	threshold_count, incident_severity, enabled, last_state, current_incident_id, row_version, created_at, updated_at`

// SecurityRuleStore administers security_rule: the admin CRUD API's
// tenant-scoped reads/writes (domain.SecurityRuleRepository) — mirroring
// AlertRuleStore's tenant-scoped role exactly. CONFIG ONLY: unlike
// AlertRuleStore, this type has no privileged-pool counterpart in this
// story — there is no detector/evaluator yet (E8.1b-2).
//
// security_rule is TENANT-OWNED — it is in TenantOwnedTables and carries
// row-level security — so this store takes no tenant argument anywhere; the
// bound connection already is the boundary (ADR-TENANCY-002).
type SecurityRuleStore struct {
	pool *pgxpool.Pool
}

// NewSecurityRuleStore builds the store over the given (tenant-scoped) pool.
func NewSecurityRuleStore(pool *pgxpool.Pool) *SecurityRuleStore {
	return &SecurityRuleStore{pool: pool}
}

var _ domain.SecurityRuleRepository = (*SecurityRuleStore)(nil)

func clampSecurityRulePage(limit int) int {
	if limit <= 0 {
		return defaultSecurityRulePageSize
	}
	if limit > maxSecurityRulePageSize {
		return maxSecurityRulePageSize
	}
	return limit
}

func scanSecurityRule(sc scanner) (*domain.SecurityRule, error) {
	var (
		r                domain.SecurityRule
		minSeverity      string
		incidentSeverity string
		lastState        string
	)
	if err := sc.Scan(
		&r.RuleID, &r.TenantID, &r.AssetID, &r.ObservationType, &minSeverity, &r.WindowSeconds,
		&r.ThresholdCount, &incidentSeverity, &r.Enabled, &lastState, &r.CurrentIncidentID,
		&r.RowVersion, &r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return nil, err
	}
	r.MinSeverity = domain.ObservationSeverity(minSeverity)
	r.IncidentSeverity = domain.IncidentSeverity(incidentSeverity)
	r.LastState = domain.SecurityRuleState(lastState)
	return &r, nil
}

// Create inserts a rule, re-verifying AssetID against this store's own
// tenant-scoped connection first — see domain.SecurityRuleRepository.Create's
// doc comment. RLS on asset filters the existence check to the caller's own
// tenant already: an id belonging to another tenant simply does not come
// back, indistinguishable from one that does not exist at all — the same
// shape AlertRuleStore.Create uses.
func (s *SecurityRuleStore) Create(ctx context.Context, r *domain.SecurityRule) (*domain.SecurityRule, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM asset WHERE asset_id = $1)`, r.AssetID,
	).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check security rule asset id: %w", err)
	}
	if !exists {
		return nil, domain.ErrNotFound
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO security_rule
			(rule_id, tenant_id, asset_id, observation_type, min_severity, window_seconds,
			 threshold_count, incident_severity, enabled, last_state, current_incident_id,
			 row_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULL,1,now(),now())
		RETURNING `+securityRuleCols,
		r.RuleID, r.TenantID, r.AssetID, r.ObservationType, string(r.MinSeverity), r.WindowSeconds,
		r.ThresholdCount, string(r.IncidentSeverity), r.Enabled, string(r.LastState))

	created, err := scanSecurityRule(row)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("insert security rule: %w", err)
	}
	return created, nil
}

// Get returns a rule by identifier, or domain.ErrNotFound.
func (s *SecurityRuleStore) Get(ctx context.Context, ruleID string) (*domain.SecurityRule, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+securityRuleCols+` FROM security_rule WHERE rule_id = $1`, ruleID)
	r, err := scanSecurityRule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get security rule: %w", err)
	}
	return r, nil
}

// List returns a page of the caller's rules, keyset-paginated over rule_id.
func (s *SecurityRuleStore) List(ctx context.Context, limit int, after string) ([]*domain.SecurityRule, error) {
	limit = clampSecurityRulePage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+securityRuleCols+`
		  FROM security_rule
		 WHERE ($1 = '' OR rule_id > $1)
		 ORDER BY rule_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list security rules: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.SecurityRule, 0, limit)
	for rows.Next() {
		r, err := scanSecurityRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan security rule: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate security rules: %w", err)
	}
	return out, nil
}

// Update changes one or more fields under optimistic locking. AssetID and
// ObservationType cannot be changed here — see domain.SecurityRulePatch's
// doc comment — nor can LastState/CurrentIncidentID, which the future
// detector's background counterpart (E8.1b-2) will own exclusively.
func (s *SecurityRuleStore) Update(
	ctx context.Context, ruleID string, rowVersion int64, patch domain.SecurityRulePatch,
) (*domain.SecurityRule, error) {
	current, err := s.Get(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	if patch.MinSeverity != nil {
		current.MinSeverity = *patch.MinSeverity
	}
	if patch.WindowSeconds != nil {
		current.WindowSeconds = *patch.WindowSeconds
	}
	if patch.ThresholdCount != nil {
		current.ThresholdCount = *patch.ThresholdCount
	}
	if patch.IncidentSeverity != nil {
		current.IncidentSeverity = *patch.IncidentSeverity
	}
	if patch.Enabled != nil {
		current.Enabled = *patch.Enabled
	}
	if err := current.Validate(); err != nil {
		return nil, err
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE security_rule
		   SET min_severity = $3, window_seconds = $4, threshold_count = $5, incident_severity = $6,
		       enabled = $7, row_version = row_version + 1, updated_at = now()
		 WHERE rule_id = $1 AND row_version = $2
		RETURNING `+securityRuleCols,
		ruleID, rowVersion, string(current.MinSeverity), current.WindowSeconds, current.ThresholdCount,
		string(current.IncidentSeverity), current.Enabled)

	updated, err := scanSecurityRule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, ruleID)
	}
	if err != nil {
		return nil, fmt.Errorf("update security rule: %w", err)
	}
	return updated, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row — mirrors AlertRuleStore.explainMissedUpdate.
func (s *SecurityRuleStore) explainMissedUpdate(ctx context.Context, ruleID string) error {
	if _, err := s.Get(ctx, ruleID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}

// Delete removes a rule, or returns domain.ErrNotFound. Carries no
// row_version — see ADR-HARD-003, which applies here unchanged (mirroring
// AlertRuleStore.Delete).
func (s *SecurityRuleStore) Delete(ctx context.Context, ruleID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM security_rule WHERE rule_id = $1`, ruleID)
	if err != nil {
		return fmt.Errorf("delete security rule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
