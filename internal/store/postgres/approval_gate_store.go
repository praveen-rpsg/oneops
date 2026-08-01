package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/policy"
)

// ApprovalGateStore answers a composed run's approval-gate quorum check
// (ADR-POLICY-002) from the admin/background pool policy.Reconciler runs on
// — the same pool PolicyStore's own background instance uses (main.go),
// which processes every tenant's runs rather than one request's, and
// therefore carries no RLS tenant binding (ADR-TENANCY-003). It reads
// exactly the two facts ADR-GOV-005's quorum already decides Approval on —
// approval_record's distinct-approver count and the tenant's
// governance_required_approvals Setting — through explicit tenant_id
// predicates rather than a bound connection's isolation, because this pool
// has none.
//
// This is a second, read-only caller of the same data
// governance.ApprovalRecorder (postgres.ApprovalStore) and domain.
// SettingRepository already serve, not a second quorum implementation: the
// CountDistinct query is identical to ApprovalStore's, made explicit-tenant,
// and RequiredApprovals decodes through the same
// domain.EffectiveRequiredApprovals every other reader of that Setting uses.
type ApprovalGateStore struct {
	pool *pgxpool.Pool
}

// NewApprovalGateStore builds the store over the given (admin) pool.
func NewApprovalGateStore(pool *pgxpool.Pool) *ApprovalGateStore {
	return &ApprovalGateStore{pool: pool}
}

var _ policy.ApprovalGate = (*ApprovalGateStore)(nil)

// CountDistinct returns the number of distinct approvers recorded for
// (tenantID, governanceID).
func (s *ApprovalGateStore) CountDistinct(ctx context.Context, tenantID, governanceID string) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(DISTINCT approver_user_id) FROM approval_record WHERE tenant_id = $1 AND governance_id = $2`,
		tenantID, governanceID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count distinct approvals: %w", err)
	}
	return n, nil
}

// RequiredApprovals returns tenantID's configured Approval quorum threshold,
// falling back to the Setting's own default when the tenant has no
// override — exactly as domain.EffectiveRequiredApprovals(nil) does for a
// caller with no SettingRepository wired at all.
func (s *ApprovalGateStore) RequiredApprovals(ctx context.Context, tenantID string) (int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+settingColumns+` FROM setting WHERE tenant_id = $1 AND key = $2`,
		tenantID, domain.GovernanceRequiredApprovalsKey)
	if err != nil {
		return 0, fmt.Errorf("get required approvals setting: %w", err)
	}
	defer rows.Close()

	var overrides []*domain.Setting
	for rows.Next() {
		st, err := scanSetting(rows)
		if err != nil {
			return 0, fmt.Errorf("scan setting: %w", err)
		}
		overrides = append(overrides, st)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate settings: %w", err)
	}
	return domain.EffectiveRequiredApprovals(overrides), nil
}
