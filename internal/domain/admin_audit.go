package domain

import (
	"context"
	"errors"
	"sort"
)

// PlatformAuditChain is the chain administrative acts take when they have no
// organisation. ADR-AUDIT-007 §6.8 mandates one chain per organisation plus
// this one; a single global chain was refused there because ADR-AUDIT-006
// serialises appends on the chain head under FOR UPDATE, so one chain would
// serialise every administrative write platform-wide.
//
// It is prefixed like the organisation chains so that the two namespaces cannot
// collide: an organisation whose id happened to be "platform" would otherwise
// share this chain.
const PlatformAuditChain = "chain:platform"

// AdminOperation is the administrative audit vocabulary. It is deliberately
// disjoint from ConfigurationOperation — these values are dotted, those are
// bare — because the two stores must never record the same fact twice.
// ck_admin_audit_operation is the same list, enforced in the database.
type AdminOperation string

const (
	AdminUserCreated         AdminOperation = "user.created"
	AdminUserProfileUpdated  AdminOperation = "user.profile_updated"
	AdminUserStatusChanged   AdminOperation = "user.status_changed"
	AdminOrgCreated          AdminOperation = "organization.created"
	AdminOrgStatusChanged    AdminOperation = "organization.status_changed"
	AdminTenantCreated       AdminOperation = "tenant.created"
	AdminTenantStatusChanged AdminOperation = "tenant.status_changed"
	AdminInvitationCreated   AdminOperation = "invitation.created"
	AdminInvitationRedeemed  AdminOperation = "invitation.redeemed"
	AdminInvitationRevoked   AdminOperation = "invitation.revoked"
	AdminMembershipGranted   AdminOperation = "membership.granted"
	AdminMembershipRevoked   AdminOperation = "membership.revoked"
)

var adminOperations = map[AdminOperation]bool{
	AdminUserCreated: true, AdminUserProfileUpdated: true, AdminUserStatusChanged: true,
	AdminOrgCreated: true, AdminOrgStatusChanged: true,
	AdminTenantCreated: true, AdminTenantStatusChanged: true,
	AdminInvitationCreated: true, AdminInvitationRedeemed: true, AdminInvitationRevoked: true,
	AdminMembershipGranted: true, AdminMembershipRevoked: true,
}

// Valid reports whether the operation is one the database will accept. Checking
// here turns a CHECK-constraint violation — which arrives as an opaque 500 —
// into a refusal the caller can read.
func (o AdminOperation) Valid() bool { return adminOperations[o] }

// AdminSubject is who or what an administrative act was performed upon.
// ADR-AUDIT-007 §6.4: these are attributes, never ownership keys. Any of them
// may be empty; an act on the tenant registry has no user, an act on a user has
// no organisation.
type AdminSubject struct {
	OrgID    string
	TenantID string
	UserID   string
}

// ChainID returns the chain this subject's acts belong on: the organisation's
// own chain when there is one, the platform chain otherwise (§6.8).
func (s AdminSubject) ChainID() string {
	if s.OrgID != "" {
		return "chain:org:" + s.OrgID
	}
	return PlatformAuditChain
}

// AdminAct is one administrative fact to be recorded. Detail carries whatever
// the caller wants preserved; the subject identifiers are merged into the
// sealed payload by the appender, so they are covered by the chain hash rather
// than sitting beside it in unsealed columns.
type AdminAct struct {
	Operation AdminOperation
	Subject   AdminSubject
	Detail    map[string]any
}

// ErrNoActor is returned when an administrative mutation reaches the audit
// chokepoint without an authenticated actor.
//
// There is deliberately no fallback here, and that is the difference between
// this and TenantIDFrom, which defaults to the system tenant so an unthreaded
// path does not 500. An audit record's whole purpose is attribution: inventing
// an actor would produce a record that says an act happened and lies about who
// performed it, which is worse than no record. ADR-AUDIT-007 §6.9 requires an
// unauditable administrative act to fail, so this error must propagate.
var ErrNoActor = errors.New("administrative act has no authenticated actor")

// ErrInvalidAdminOperation is returned for an operation outside the vocabulary.
var ErrInvalidAdminOperation = errors.New("operation is not an administrative audit operation")

type actorContextKey struct{}

// WithActor returns ctx carrying the authenticated principal's identity. Set
// once, at the transport edge, exactly as WithTenant is.
func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorContextKey{}, actor)
}

// ActorFrom returns the authenticated actor. The boolean is false when none was
// set or it is empty — callers must refuse the act rather than substitute one.
func ActorFrom(ctx context.Context) (string, bool) {
	a, ok := ctx.Value(actorContextKey{}).(string)
	if !ok || a == "" {
		return "", false
	}
	return a, true
}

// AdminChainIDs returns the distinct chains a set of acts touches, in ascending
// order.
//
// The ordering is the point, not a convenience. One administrative act can
// touch two chains — creating an organisation writes both `tenant` (platform
// chain) and `organization` (its own chain), and both are in §6.2's scope — and
// each append takes FOR UPDATE on a chain head. Two transactions acquiring the
// same two heads in opposite orders deadlock; reproduced live against this
// schema, with the loser aborting after 1.6s. A single total order over the
// chain ids removes the cycle, so every transaction queues instead of
// deadlocking. No such rule existed anywhere in the tree before this.
func AdminChainIDs(acts []AdminAct) []string {
	seen := make(map[string]bool, len(acts))
	ids := make([]string, 0, len(acts))
	for _, a := range acts {
		id := a.Subject.ChainID()
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
