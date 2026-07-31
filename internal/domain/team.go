package domain

import (
	"context"
	"strings"
	"time"
)

// TeamStatus is the lifecycle of a team.
//
// Modelled on OrganizationStatus rather than MembershipStatus: a Team is a
// governed object with a name and a lifecycle of its own, not a grant.
// Archiving rather than deleting keeps the row so anything that already names
// this team — a team membership, a report — is not orphaned, the same reason
// Organization has no hard delete (ADR-IDENTITY-001 §8.3).
type TeamStatus string

const (
	TeamActive   TeamStatus = "active"
	TeamArchived TeamStatus = "archived"
)

// Valid reports whether s is a defined status.
func (s TeamStatus) Valid() bool {
	return s == TeamActive || s == TeamArchived
}

// MaxTeamNameLength bounds a free-text field that reaches consoles and logs,
// the same bound User.DisplayName carries.
const MaxTeamNameLength = 200

// Team groups Users within an Organisation.
//
// TenantID is denormalised from the organisation rather than joined, exactly
// as Membership's is and for the same reason: it is the RLS key, and a policy
// that had to read the organisation mapping to reach it would make that
// mapping a dependency of every isolation decision (ADR-IDENTITY-002 §6).
//
// Team is NOT an authorisation fact. Membership alone answers "does this user
// belong to this organisation" (ADR-IDENTITY-002 §2.3); a Team only groups
// users who are already members, which is why TeamMembership below carries no
// status of its own and a member can simply be removed.
type Team struct {
	TeamID     string
	TenantID   string
	OrgID      string
	Name       string
	Status     TeamStatus
	RowVersion int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Active reports whether the team is in ordinary use.
func (t *Team) Active() bool { return t.Status == TeamActive }

// Validate enforces the invariants the database also enforces, so a bad
// request fails with a field-level 422 rather than a constraint-violation 500.
func (t *Team) Validate() error {
	if strings.TrimSpace(t.TeamID) == "" {
		return newValidation("team_id", "must not be empty")
	}
	if strings.TrimSpace(t.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(t.OrgID) == "" {
		return newValidation("org_id", "must not be empty")
	}
	name := strings.TrimSpace(t.Name)
	if name == "" {
		return newValidation("name", "must not be empty")
	}
	if len(name) > MaxTeamNameLength {
		return newValidation("name", "must be at most 200 characters")
	}
	if !t.Status.Valid() {
		return newValidation("status", "must be one of: active, archived")
	}
	return nil
}

// NewTeam builds an active team.
//
// tenantID is a parameter rather than something this constructor derives,
// because the only correct source is the record that already carries it — the
// organisation the team belongs to. Deriving it here would mean guessing, and
// a team labelled with the wrong tenant is a grant of access to the wrong
// boundary.
func NewTeam(orgID, tenantID, name string) (*Team, error) {
	t := &Team{
		TeamID:   NewID(),
		TenantID: strings.TrimSpace(tenantID),
		OrgID:    strings.TrimSpace(orgID),
		Name:     strings.TrimSpace(name),
		Status:   TeamActive,
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return t, nil
}

// TeamMembership joins a User to a Team.
//
// Unlike Membership, this is a plain associative row rather than an
// authorisation fact: no audit record's "who authorised this act" points at a
// team_membership_id, because Membership alone is what grants access to an
// organisation's boundary. A team only groups users who are already members,
// so there is no lifecycle to preserve and RemoveMember is a real delete
// rather than a status change.
type TeamMembership struct {
	TeamMembershipID string
	TenantID         string
	TeamID           string
	UserID           string
	RowVersion       int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Validate enforces the invariants the database also enforces.
func (m *TeamMembership) Validate() error {
	if strings.TrimSpace(m.TeamMembershipID) == "" {
		return newValidation("team_membership_id", "must not be empty")
	}
	if strings.TrimSpace(m.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(m.TeamID) == "" {
		return newValidation("team_id", "must not be empty")
	}
	if strings.TrimSpace(m.UserID) == "" {
		return newValidation("user_id", "must not be empty")
	}
	return nil
}

// NewTeamMembership builds a team membership.
//
// tenantID is a parameter for the same reason it is on NewMembership: the only
// correct source is the record that already carries it — here, the team being
// joined.
func NewTeamMembership(teamID, tenantID, userID string) (*TeamMembership, error) {
	m := &TeamMembership{
		TeamMembershipID: NewID(),
		TenantID:         strings.TrimSpace(tenantID),
		TeamID:           strings.TrimSpace(teamID),
		UserID:           strings.TrimSpace(userID),
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// TeamRepository administers teams and their membership.
//
// team and team_membership are TENANT-OWNED: both are in TenantOwnedTables and
// carry row-level security, so this repository takes no tenant argument
// anywhere — the bound connection already is the boundary (ADR-TENANCY-002).
//
// Unlike MembershipRepository, mutations here do not pass through the
// administrative-audit chokepoint (withAdminAudit). ADR-AUDIT-007 §6.2 scopes
// that store to exactly five named tables (app_user, organization, membership,
// invitation, tenant) — the identity-governance facts a bearer token's
// boundary is resolved from — and §9 of that ADR treats widening §6.2 as "an
// amendment to this ADR's scope table", not a decision a store gets to make
// for itself. Team is organisational structure, not an authorisation fact, so
// it follows the same pattern as webhook and policy: tenant-owned, RLS, and
// optimistic locking, without the administrative chokepoint.
type TeamRepository interface {
	Create(ctx context.Context, t *Team) (*Team, error)
	Get(ctx context.Context, teamID string) (*Team, error)
	ListByOrg(ctx context.Context, orgID string, limit int, after string) ([]*Team, error)
	// UpdateProfile renames a team under optimistic locking.
	UpdateProfile(ctx context.Context, teamID string, rowVersion int64, name string) (*Team, error)
	// SetStatus archives or reactivates a team under optimistic locking.
	SetStatus(ctx context.Context, teamID string, rowVersion int64, status TeamStatus) (*Team, error)

	// AddMember binds a user to a team. Adding a user who is already a member is
	// idempotent and returns the existing row.
	AddMember(ctx context.Context, m *TeamMembership) (*TeamMembership, error)
	// RemoveMember unbinds a user from a team. Unlike Membership.Revoke, this is
	// a real delete: see the TeamMembership doc comment for why.
	RemoveMember(ctx context.Context, teamID, userID string) error
	// ListMembers returns a page of a team's members.
	ListMembers(ctx context.Context, teamID string, limit int, after string) ([]*TeamMembership, error)
}
