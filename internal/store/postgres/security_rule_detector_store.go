package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/security"
)

// SecurityRuleDetectorStore is the leader-gated SecurityDetector's own
// privileged surface over security_rule (security.Store, E8.1b-2) — built
// over the PRIVILEGED pool, DELIBERATELY SEPARATE from SecurityRuleStore
// (the admin CRUD API, tenant-scoped) rather than one dual-role struct the
// way AlertRuleStore/alerting.Store share a type. See SecurityRuleStore's own
// doc comment for why: keeping this type's method set to exactly
// EnabledRules/RecordTransition means Create's asset-existence probe is
// never reachable from a privileged instance, so it needs no
// privilegedReadExemptions entry — the smallest privileged surface that can
// satisfy security.Store, not merely the one that happens to share code with
// the admin type.
//
// security_rule is TENANT-OWNED and carries row-level security; this type's
// queries carry no tenant_id predicate of their own because none of
// EnabledRules/RecordTransition filter by asset_id or any other
// tenant-shared key — EnabledRules pages every tenant's rules by design (the
// detector's due-scan), and RecordTransition is a single-row-by-primary-key
// CAS, the same shape AlertRuleStore.RecordTransition's own doc comment
// explains needs no additional owning predicate.
type SecurityRuleDetectorStore struct {
	pool *pgxpool.Pool
}

// NewSecurityRuleDetectorStore builds the store over the given (privileged)
// pool.
func NewSecurityRuleDetectorStore(pool *pgxpool.Pool) *SecurityRuleDetectorStore {
	return &SecurityRuleDetectorStore{pool: pool}
}

var _ security.Store = (*SecurityRuleDetectorStore)(nil)

// EnabledRules implements security.Store: enabled rules, across every
// tenant, keyset-paginated over rule_id — mirrors
// AlertRuleStore.EnabledRules exactly.
func (s *SecurityRuleDetectorStore) EnabledRules(ctx context.Context, limit int, after string) ([]*domain.SecurityRule, error) {
	limit = clampSecurityRulePage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+securityRuleCols+`
		  FROM security_rule
		 WHERE enabled = true AND ($1 = '' OR rule_id > $1)
		 ORDER BY rule_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list enabled security rules: %w", err)
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
		return nil, fmt.Errorf("iterate enabled security rules: %w", err)
	}
	return out, nil
}

// RecordTransition implements security.Store: a fenced, single-row CAS
// update of last_state/last_transition_at/current_incident_id, keyed on the
// rule's own primary key — mirrors AlertRuleStore.RecordTransition exactly,
// including currentIncidentID travelling in the SAME statement as the state
// transition (see that method's doc comment for why).
func (s *SecurityRuleDetectorStore) RecordTransition(
	ctx context.Context, ruleID string, rowVersion int64, state domain.SecurityRuleState, at time.Time, currentIncidentID *string,
) (*domain.SecurityRule, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE security_rule
		   SET last_state = $3, last_transition_at = $4, current_incident_id = $5,
		       row_version = row_version + 1, updated_at = now()
		 WHERE rule_id = $1 AND row_version = $2
		RETURNING `+securityRuleCols,
		ruleID, rowVersion, string(state), at, currentIncidentID)

	updated, err := scanSecurityRule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, ruleID)
	}
	if err != nil {
		return nil, fmt.Errorf("record security rule transition: %w", err)
	}
	return updated, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row — mirrors SecurityRuleStore.explainMissedUpdate,
// duplicated rather than shared across the two types for the identical
// reason their whole method sets are kept separate (see this type's own doc
// comment): a helper spanning both would recreate the coupling the split was
// meant to remove.
func (s *SecurityRuleDetectorStore) explainMissedUpdate(ctx context.Context, ruleID string) error {
	row := s.pool.QueryRow(ctx, `SELECT `+securityRuleCols+` FROM security_rule WHERE rule_id = $1`, ruleID)
	if _, err := scanSecurityRule(row); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrNotFound
		}
		return fmt.Errorf("get security rule: %w", err)
	}
	return domain.ErrVersionMismatch
}
