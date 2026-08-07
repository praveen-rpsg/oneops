package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

const (
	defaultSecurityResponseRulePageSize = 50
	maxSecurityResponseRulePageSize     = 500
)

const securityResponseRuleCols = `rule_id, tenant_id, name, min_severity, asset_id, action_type, action_config,
	enabled, row_version, created_at, updated_at`

// SecurityResponseRuleStore administers security_response_rule: the admin
// CRUD API's tenant-scoped reads/writes (domain.SecurityResponseRuleRepository)
// — built EXCLUSIVELY over the tenant-scoped pool, unlike SecurityRuleStore's
// own dual-role split. The leader-gated SecurityResponder's own privileged
// due-scan surface is *SecurityResponderStore instead — a DELIBERATELY
// SEPARATE type (security_responder_store.go), mirroring
// SecurityRuleDetectorStore/IOCMatcherStore's identical split from their own
// admin CRUD stores, for the identical reason: keeping this type's method
// set to exactly Create/Get/List/Update/Delete means this store's own
// asset-existence probe (Create, below) is never attributed to a privileged
// instance by TestPrivilegedReads_AreScopedToATenant.
//
// security_response_rule is TENANT-OWNED — it is in TenantOwnedTables and
// carries row-level security — so this store takes no tenant argument
// anywhere; the bound connection already is the boundary (ADR-TENANCY-002).
type SecurityResponseRuleStore struct {
	pool *pgxpool.Pool
}

// NewSecurityResponseRuleStore builds the store over the given (tenant-scoped) pool.
func NewSecurityResponseRuleStore(pool *pgxpool.Pool) *SecurityResponseRuleStore {
	return &SecurityResponseRuleStore{pool: pool}
}

var _ domain.SecurityResponseRuleRepository = (*SecurityResponseRuleStore)(nil)

func clampSecurityResponseRulePage(limit int) int {
	if limit <= 0 {
		return defaultSecurityResponseRulePageSize
	}
	if limit > maxSecurityResponseRulePageSize {
		return maxSecurityResponseRulePageSize
	}
	return limit
}

func scanSecurityResponseRule(sc scanner) (*domain.SecurityResponseRule, error) {
	var (
		r            domain.SecurityResponseRule
		minSeverity  string
		actionConfig []byte
	)
	if err := sc.Scan(
		&r.RuleID, &r.TenantID, &r.Name, &minSeverity, &r.AssetID, &r.ActionType, &actionConfig,
		&r.Enabled, &r.RowVersion, &r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return nil, err
	}
	r.MinSeverity = domain.IncidentSeverity(minSeverity)
	r.ActionConfig = json.RawMessage(actionConfig)
	return &r, nil
}

// Create inserts a rule, re-verifying AssetID (when set) against this
// store's own tenant-scoped connection first — see
// domain.SecurityResponseRuleRepository.Create's doc comment. RLS on asset
// filters the existence check to the caller's own tenant already: an id
// belonging to another tenant simply does not come back, indistinguishable
// from one that does not exist at all — the same shape SecurityRuleStore.
// Create/IncidentStore.Create use for their own optional/required asset
// references.
func (s *SecurityResponseRuleStore) Create(ctx context.Context, r *domain.SecurityResponseRule) (*domain.SecurityResponseRule, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.AssetID != nil {
		var exists bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM asset WHERE asset_id = $1)`, *r.AssetID,
		).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check security response rule asset id: %w", err)
		}
		if !exists {
			return nil, domain.ErrNotFound
		}
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO security_response_rule
			(rule_id, tenant_id, name, min_severity, asset_id, action_type, action_config,
			 enabled, row_version, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,1,now(),now())
		RETURNING `+securityResponseRuleCols,
		r.RuleID, r.TenantID, r.Name, string(r.MinSeverity), r.AssetID, r.ActionType,
		actionConfigJSON(r.ActionConfig), r.Enabled)

	created, err := scanSecurityResponseRule(row)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("insert security response rule: %w", err)
	}
	return created, nil
}

// Get returns a rule by identifier, or domain.ErrNotFound.
func (s *SecurityResponseRuleStore) Get(ctx context.Context, ruleID string) (*domain.SecurityResponseRule, error) {
	row := s.pool.QueryRow(ctx, `SELECT `+securityResponseRuleCols+` FROM security_response_rule WHERE rule_id = $1`, ruleID)
	r, err := scanSecurityResponseRule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get security response rule: %w", err)
	}
	return r, nil
}

// List returns a page of the caller's rules, keyset-paginated over rule_id.
func (s *SecurityResponseRuleStore) List(ctx context.Context, limit int, after string) ([]*domain.SecurityResponseRule, error) {
	limit = clampSecurityResponseRulePage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+securityResponseRuleCols+`
		  FROM security_response_rule
		 WHERE ($1 = '' OR rule_id > $1)
		 ORDER BY rule_id
		 LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list security response rules: %w", err)
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
		return nil, fmt.Errorf("iterate security response rules: %w", err)
	}
	return out, nil
}

// Update changes one or more fields under optimistic locking. AssetID and
// ActionType cannot be changed here — see domain.SecurityResponseRulePatch's
// doc comment — delete and recreate the rule instead.
func (s *SecurityResponseRuleStore) Update(
	ctx context.Context, ruleID string, rowVersion int64, patch domain.SecurityResponseRulePatch,
) (*domain.SecurityResponseRule, error) {
	current, err := s.Get(ctx, ruleID)
	if err != nil {
		return nil, err
	}
	if patch.Name != nil {
		current.Name = *patch.Name
	}
	if patch.MinSeverity != nil {
		current.MinSeverity = *patch.MinSeverity
	}
	if patch.ActionConfig != nil {
		current.ActionConfig = *patch.ActionConfig
	}
	if patch.Enabled != nil {
		current.Enabled = *patch.Enabled
	}
	if err := current.Validate(); err != nil {
		return nil, err
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE security_response_rule
		   SET name = $3, min_severity = $4, action_config = $5, enabled = $6,
		       row_version = row_version + 1, updated_at = now()
		 WHERE rule_id = $1 AND row_version = $2
		RETURNING `+securityResponseRuleCols,
		ruleID, rowVersion, current.Name, string(current.MinSeverity),
		actionConfigJSON(current.ActionConfig), current.Enabled)

	updated, err := scanSecurityResponseRule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, s.explainMissedUpdate(ctx, ruleID)
	}
	if err != nil {
		return nil, fmt.Errorf("update security response rule: %w", err)
	}
	return updated, nil
}

// explainMissedUpdate distinguishes "gone" from "stale version" after an
// UPDATE matched no row — mirrors SecurityRuleStore.explainMissedUpdate.
func (s *SecurityResponseRuleStore) explainMissedUpdate(ctx context.Context, ruleID string) error {
	if _, err := s.Get(ctx, ruleID); err != nil {
		return err
	}
	return domain.ErrVersionMismatch
}

// Delete removes a rule, or returns domain.ErrNotFound. Carries no
// row_version — see ADR-HARD-003, which applies here unchanged (mirroring
// SecurityRuleStore.Delete).
func (s *SecurityResponseRuleStore) Delete(ctx context.Context, ruleID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM security_response_rule WHERE rule_id = $1`, ruleID)
	if err != nil {
		return fmt.Errorf("delete security response rule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// actionConfigJSON normalises an empty/nil ActionConfig to "{}" before it
// reaches the jsonb column — mirrors policy_store.go's actionConfig helper,
// duplicated rather than shared across packages for the same reason this
// package duplicates other small helpers rather than importing internal/policy
// for a one-line function.
func actionConfigJSON(c json.RawMessage) []byte {
	if len(c) == 0 {
		return []byte("{}")
	}
	return c
}
