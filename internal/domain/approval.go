package domain

import (
	"context"
	"strings"
	"time"
)

// ApprovalRecord is one approver's DISTINCT approval of a governance object
// under the §8 Approval operation. It is the unit a tenant's
// governance_required_approvals quorum (Setting, see setting.go) is counted
// over — the Governance Engine (internal/governance) requires N distinct
// ApprovalRecords for a governance object before it transitions to Approved
// (ADR-GOV-005).
//
// ApproverUserID is the approving actor's identity as the governance engine
// already knows it — governance.Command.Actor, which is the verified JWT
// `sub` (internal/auth/jwt.go) passed straight through by
// handlers_governance.go. It is deliberately NOT modelled as a reference to
// app_user: this platform has never resolved a token subject against
// app_user anywhere, so requiring that binding here would make Approval fail
// for exactly the identities that already exercise it today (the dev-mode
// "system" actor, and any JWT subject not separately provisioned as an
// app_user row). See ADR-GOV-005 for the follow-up this leaves open.
type ApprovalRecord struct {
	ApprovalID     string
	TenantID       string
	GovernanceID   string
	ApproverUserID string
	CreatedAt      time.Time
}

// Validate enforces the invariants the database also enforces, so a bad
// record fails with a field-level error rather than a constraint-violation.
func (a *ApprovalRecord) Validate() error {
	if strings.TrimSpace(a.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(a.GovernanceID) == "" {
		return newValidation("governance_id", "must not be empty")
	}
	if strings.TrimSpace(a.ApproverUserID) == "" {
		return newValidation("approver_user_id", "must not be empty")
	}
	return nil
}

// NewApprovalRecord builds one approver's approval of a governance object.
func NewApprovalRecord(tenantID, governanceID, approverUserID string) (*ApprovalRecord, error) {
	a := &ApprovalRecord{
		ApprovalID:     NewID(),
		TenantID:       strings.TrimSpace(tenantID),
		GovernanceID:   strings.TrimSpace(governanceID),
		ApproverUserID: strings.TrimSpace(approverUserID),
	}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return a, nil
}

// ApprovalRepository is the read-side port for a governance object's
// recorded approvals — GET /governance/{id}/approvals. The write side lives
// entirely inside the Governance Engine's own transaction
// (governance.ApprovalRecorder), because recording an approval and counting
// it toward quorum must never race with a concurrent approver on the same
// object; this port exists only to answer "who has approved so far", after
// the fact.
//
// approval_record is TENANT-OWNED — it is in postgres.TenantOwnedTables and
// carries row-level security — so ListApprovers takes no tenant argument:
// the bound connection is the boundary, the same shape TeamRepository and
// SettingRepository use (ADR-TENANCY-002).
type ApprovalRepository interface {
	// ListApprovers returns every distinct approval recorded for governanceID,
	// ordered by CreatedAt so the sequence a client sees is deterministic.
	ListApprovers(ctx context.Context, governanceID string) ([]*ApprovalRecord, error)
}
