package domain

import (
	"strings"
	"time"
)

// MembershipStatus is the lifecycle of a membership.
//
// There are two states and no delete. Revocation is a status change because the
// audit chain must outlive the membership that authorised the acts it records
// (ADR-IDENTITY-001 §8.3) — removing the row would orphan events whose author
// can no longer be explained.
type MembershipStatus string

const (
	MembershipActive  MembershipStatus = "active"
	MembershipRevoked MembershipStatus = "revoked"
)

// Valid reports whether s is a defined status.
func (s MembershipStatus) Valid() bool {
	return s == MembershipActive || s == MembershipRevoked
}

// Membership joins a User to an Organisation (ADR-IDENTITY-002 §2.3).
//
// It is THE authorisation fact. A request's tenant is resolved by reading this
// row and never taken from the request itself: a tenant identifier in a header
// or a body is a claim, this is the fact (ADR-IDENTITY-002 §4.5, ADR-TENANCY-004).
//
// TenantID is denormalised from the organisation rather than joined. It is the
// RLS key, and a policy that had to read the organisation mapping to reach it
// would make that mapping a dependency of every isolation decision — a second
// path to ownership (ADR-IDENTITY-002 §6).
type Membership struct {
	MembershipID string
	TenantID     string
	OrgID        string
	UserID       string
	Status       MembershipStatus
	RowVersion   int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Active reports whether this membership currently grants access.
func (m *Membership) Active() bool { return m.Status == MembershipActive }

// Validate enforces the invariants the database also enforces.
func (m *Membership) Validate() error {
	if strings.TrimSpace(m.MembershipID) == "" {
		return newValidation("membership_id", "must not be empty")
	}
	if strings.TrimSpace(m.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(m.OrgID) == "" {
		return newValidation("org_id", "must not be empty")
	}
	if strings.TrimSpace(m.UserID) == "" {
		return newValidation("user_id", "must not be empty")
	}
	if !m.Status.Valid() {
		return newValidation("status", "must be one of: active, revoked")
	}
	return nil
}

// NewMembership builds an active membership.
//
// tenantID is a parameter rather than something this constructor derives,
// because the only correct source is the record that already carries it — the
// organisation, or the invitation being redeemed. Deriving it here would mean
// guessing, and a membership labelled with the wrong tenant is a grant of
// access to the wrong boundary.
func NewMembership(orgID, tenantID, userID string) (*Membership, error) {
	m := &Membership{
		MembershipID: NewID(),
		TenantID:     strings.TrimSpace(tenantID),
		OrgID:        strings.TrimSpace(orgID),
		UserID:       strings.TrimSpace(userID),
		Status:       MembershipActive,
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}
