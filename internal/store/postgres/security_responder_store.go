package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/security"
)

const (
	defaultSecurityResponderRulePageSize     = 200
	maxSecurityResponderRulePageSize         = 500
	defaultSecurityResponderIncidentPageSize = 500
	maxSecurityResponderIncidentPageSize     = 500
)

func clampSecurityResponderRulePage(limit int) int {
	if limit <= 0 {
		return defaultSecurityResponderRulePageSize
	}
	if limit > maxSecurityResponderRulePageSize {
		return maxSecurityResponderRulePageSize
	}
	return limit
}

func clampSecurityResponderIncidentPage(limit int) int {
	if limit <= 0 {
		return defaultSecurityResponderIncidentPageSize
	}
	if limit > maxSecurityResponderIncidentPageSize {
		return maxSecurityResponderIncidentPageSize
	}
	return limit
}

// SecurityResponderStore is the leader-gated Responder's own
// privileged surface (E8.5, ADR-SOC-010) — built over the PRIVILEGED pool,
// DELIBERATELY SEPARATE from SecurityResponseRuleStore (the admin CRUD API,
// tenant-scoped) rather than one dual-role struct, mirroring
// SecurityRuleDetectorStore's/IOCMatcherStore's identical split from their
// own admin CRUD stores: keeping this type's method set to exactly
// TenantsWithEnabledSecurityResponseRules/
// EnabledSecurityResponseRulesForTenant/RecentSecurityIncidentsForTenant/
// ClaimDispatch means SecurityResponseRuleStore.Create's asset-existence
// probe is never reachable from a privileged instance.
//
// security_response_rule/incident/security_response_dispatch are all
// TENANT-OWNED; every query here carries an EXPLICIT tenant_id predicate or
// is deliberately tenant-agnostic (TenantsWithEnabledSecurityResponseRules,
// which discloses only which tenants have work) — this connection has
// row-level security switched off (ADR-TENANCY-012).
type SecurityResponderStore struct {
	pool *pgxpool.Pool
}

// NewSecurityResponderStore builds the store over the given (privileged) pool.
func NewSecurityResponderStore(pool *pgxpool.Pool) *SecurityResponderStore {
	return &SecurityResponderStore{pool: pool}
}

var (
	_ security.ResponseRuleLister   = (*SecurityResponderStore)(nil)
	_ security.IncidentWindowReader = (*SecurityResponderStore)(nil)
	_ security.DispatchClaimer      = (*SecurityResponderStore)(nil)
)

// TenantsWithEnabledSecurityResponseRules implements
// security.ResponseRuleLister — mirrors IOCMatcherStore.TenantsWithEnabledIOCs:
// tenant-agnostic, discloses only which tenants have work.
func (s *SecurityResponderStore) TenantsWithEnabledSecurityResponseRules(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT tenant_id
		  FROM security_response_rule
		 WHERE enabled = true
		 ORDER BY tenant_id`)
	if err != nil {
		return nil, fmt.Errorf("list tenants with enabled security response rules: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, fmt.Errorf("scan tenant_id: %w", err)
		}
		out = append(out, tenantID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenants with enabled security response rules: %w", err)
	}
	return out, nil
}

// EnabledSecurityResponseRulesForTenant implements
// security.ResponseRuleLister: up to limit of tenantID's enabled rules,
// keyset-paginated over rule_id, with an EXPLICIT tenant_id predicate
// (ADR-TENANCY-012).
func (s *SecurityResponderStore) EnabledSecurityResponseRulesForTenant(
	ctx context.Context, tenantID string, limit int, after string,
) ([]*domain.SecurityResponseRule, error) {
	limit = clampSecurityResponderRulePage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+securityResponseRuleCols+`
		  FROM security_response_rule
		 WHERE tenant_id = $1 AND enabled = true AND ($2 = '' OR rule_id > $2)
		 ORDER BY rule_id
		 LIMIT $3`, tenantID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list enabled security response rules for tenant: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.SecurityResponseRule, 0, limit)
	for rows.Next() {
		r, err := scanSecurityResponseRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan security response rule: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate enabled security response rules for tenant: %w", err)
	}
	return out, nil
}

// RecentSecurityIncidentsForTenant implements
// security.IncidentWindowReader: up to limit of tenantID's
// security-sourced incidents with created_at in (from, to], ordered and
// keyset-paginated over incident_id (a ULID, and therefore chronological).
// EXPLICIT tenant_id AND source predicates (ADR-TENANCY-012): this
// connection has row-level security switched off, so nothing else confines
// this read to tenantID, and nothing else confines it to the security
// source this engine is scoped to (see Responder's own doc
// comment).
func (s *SecurityResponderStore) RecentSecurityIncidentsForTenant(
	ctx context.Context, tenantID string, from, to time.Time, limit int, after *domain.Incident,
) ([]domain.Incident, error) {
	limit = clampSecurityResponderIncidentPage(limit)
	afterID := ""
	if after != nil {
		afterID = after.IncidentID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+incidentColumns+`
		  FROM incident
		 WHERE tenant_id = $1 AND source = 'security'
		   AND created_at > $2 AND created_at <= $3
		   AND ($4 = '' OR incident_id > $4)
		 ORDER BY incident_id
		 LIMIT $5`, tenantID, from, to, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent security incidents for tenant: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Incident, 0, limit)
	for rows.Next() {
		inc, err := scanIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("scan incident: %w", err)
		}
		out = append(out, *inc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent security incidents for tenant: %w", err)
	}
	return out, nil
}

// ClaimDispatch implements security.DispatchClaimer: a record-first,
// insert-before-execute claim of one (tenantID, incidentID, ruleID) triple
// against security_response_dispatch's UNIQUE (tenant_id, incident_id,
// rule_id) constraint — see that port's own doc comment for why this
// ordering, not run-then-record, is the right trade for a SAFE action that
// is not naturally idempotent.
//
// Locks incidentID's row FOR UPDATE before inserting — this ledger's own
// "chain head" (ADR-AUDIT-006's discipline, the same parent-row lock
// IncidentStore.recordEvent/ComplianceControlStore.AddEvidence take before
// appending their own child row): it serialises every ClaimDispatch attempt
// for the SAME incident, so two rules matching the SAME incident at the
// SAME instant, from different Responder goroutines, insert their
// (distinct) dispatch rows one at a time rather than racing the unique
// constraint blind. This connection has row-level security switched off, so
// the lock is ordinary transactional serialisation, not a tenant boundary —
// tenantID is still an explicit column on the INSERT itself.
func (s *SecurityResponderStore) ClaimDispatch(
	ctx context.Context, tenantID, incidentID, ruleID, actionType string, at time.Time,
) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lockedIncidentID string
	if err := tx.QueryRow(ctx, `SELECT incident_id FROM incident WHERE incident_id = $1 FOR UPDATE`, incidentID).
		Scan(&lockedIncidentID); err != nil {
		return false, fmt.Errorf("lock incident for dispatch claim: %w", err)
	}

	claimed, err := s.insertDispatch(ctx, tx, tenantID, incidentID, ruleID, actionType, at)
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit: %w", err)
	}
	return claimed, nil
}

// insertDispatch performs ClaimDispatch's actual INSERT, inside the caller's
// transaction and under the incident-row lock it already holds.
// RowsAffected()==0 means the row already existed (ON CONFLICT DO NOTHING
// skipped it): an earlier pass, or a concurrent goroutine that raced the
// lock, already claimed this pair.
func (s *SecurityResponderStore) insertDispatch(
	ctx context.Context, tx pgx.Tx, tenantID, incidentID, ruleID, actionType string, at time.Time,
) (bool, error) {
	tag, err := tx.Exec(ctx, `
		INSERT INTO security_response_dispatch (dispatch_id, tenant_id, incident_id, rule_id, action_type, dispatched_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (tenant_id, incident_id, rule_id) DO NOTHING`,
		domain.NewID(), tenantID, incidentID, ruleID, actionType, at)
	if err != nil {
		return false, fmt.Errorf("claim security response dispatch: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
