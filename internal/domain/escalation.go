package domain

import (
	"context"
	"strings"
	"time"
)

// EscalationPolicyStatus is the lifecycle of an escalation policy. Modelled on
// OnCallScheduleStatus/TeamStatus, not on a richer state machine: a policy is
// a governed, named object an operator archives rather than deletes so
// anything already pointing at it is not orphaned (the same reasoning
// OnCallSchedule's own doc comment applies).
type EscalationPolicyStatus string

const (
	EscalationPolicyActive   EscalationPolicyStatus = "active"
	EscalationPolicyArchived EscalationPolicyStatus = "archived"
)

// Valid reports whether s is a defined status.
func (s EscalationPolicyStatus) Valid() bool {
	return s == EscalationPolicyActive || s == EscalationPolicyArchived
}

// MaxEscalationPolicyNameLength bounds a free-text field that reaches
// consoles and logs, the same bound OnCallSchedule.Name carries.
const MaxEscalationPolicyNameLength = 200

// MinEscalationWaitSeconds is the floor on how long a tier waits for an
// acknowledgement before the (E5.2b-2) engine advances to the next tier.
// Zero or negative would make "wait, then advance" meaningless.
const MinEscalationWaitSeconds = 1

// EscalationPolicy is a named, ordered chain of tiers (EscalationTier) an
// incident is escalated through: page tier 0's schedule, wait
// WaitSeconds for an acknowledgement, then advance to tier 1, and so on.
//
// This is a real, first-class operational object an operator declares — not
// a reified reduced-noun (docs/PLATFORM-BUILD-PLAN.md §4, Vol II §5.3),
// exactly the reasoning OnCallSchedule's own doc comment applies to a
// different shape of rotation configuration. The MVP shape is one
// tenant-default policy, but the table itself is general: a tenant may
// declare more than one. Per-asset/per-service ROUTING to a specific policy
// (which policy applies to which incident) is explicitly out of this
// story's scope — see docs/PLATFORM-BUILD-PLAN.md E5.2b-2.
//
// Config only: this story stores policies and tiers. It runs no engine, no
// worker, pages nobody, and touches no incident or notification — that is
// E5.2b-2, entirely.
type EscalationPolicy struct {
	PolicyID   string
	TenantID   string
	Name       string
	Status     EscalationPolicyStatus
	RowVersion int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Active reports whether the policy is in ordinary use.
func (p *EscalationPolicy) Active() bool { return p.Status == EscalationPolicyActive }

// Validate enforces the invariants the database also enforces, so a bad
// request fails with a field-level 422 rather than a constraint-violation 500.
func (p *EscalationPolicy) Validate() error {
	if strings.TrimSpace(p.PolicyID) == "" {
		return newValidation("policy_id", "must not be empty")
	}
	if strings.TrimSpace(p.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return newValidation("name", "must not be empty")
	}
	if len(name) > MaxEscalationPolicyNameLength {
		return newValidation("name", "must be at most 200 characters")
	}
	if !p.Status.Valid() {
		return newValidation("status", "must be one of: active, archived")
	}
	return nil
}

// NewEscalationPolicy builds a freshly minted, active policy.
func NewEscalationPolicy(tenantID, name string) (*EscalationPolicy, error) {
	p := &EscalationPolicy{
		PolicyID: NewID(),
		TenantID: strings.TrimSpace(tenantID),
		Name:     strings.TrimSpace(name),
		Status:   EscalationPolicyActive,
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// EscalationPolicyPatch changes one or more fields of a policy under
// optimistic locking. Every field is a pointer so an absent field is
// distinguishable from an explicit zero value — the same shape
// OnCallSchedulePatch uses.
type EscalationPolicyPatch struct {
	Name   *string
	Status *EscalationPolicyStatus
}

// EscalationTier is one rung in a policy's escalation ladder: page
// OnCallScheduleID's current on-call user, wait WaitSeconds for an
// acknowledgement, then (E5.2b-2) advance to the next tier. Position is
// 0-based and contiguous within a policy (0..N-1): AddTier appends at N,
// RemoveTier closes the resulting gap, and ReorderTiers rewrites the whole
// sequence atomically — the identical contract OnCallParticipant's own doc
// comment describes for a rotation roster.
type EscalationTier struct {
	TierID   string
	TenantID string
	PolicyID string
	Position int
	// OnCallScheduleID names the schedule this tier pages — re-verified to
	// belong to the caller's tenant before this row is written
	// (EscalationPolicyRepository.AddTier's doc comment), the identical
	// defense OnCallScheduleStore.AddParticipant applies to a participant's
	// user_id (ADR-ASSET-001 §6, ADR-ONCALL-001 §5).
	OnCallScheduleID string
	// WaitSeconds is how long the (E5.2b-2) engine waits for an
	// acknowledgement at this tier before advancing to the next one.
	WaitSeconds int
	RowVersion  int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Validate enforces the invariants the database also enforces.
func (t *EscalationTier) Validate() error {
	if strings.TrimSpace(t.TierID) == "" {
		return newValidation("tier_id", "must not be empty")
	}
	if strings.TrimSpace(t.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(t.PolicyID) == "" {
		return newValidation("policy_id", "must not be empty")
	}
	if strings.TrimSpace(t.OnCallScheduleID) == "" {
		return newValidation("on_call_schedule_id", "must not be empty")
	}
	if t.Position < 0 {
		return newValidation("position", "must not be negative")
	}
	if t.WaitSeconds < MinEscalationWaitSeconds {
		return newValidation("wait_seconds", "must be greater than 0")
	}
	return nil
}

// NewEscalationTier builds a tier at the given position. The repository, not
// this constructor, decides what that position is (append at the current
// count) — see EscalationPolicyRepository.AddTier.
func NewEscalationTier(policyID, tenantID, onCallScheduleID string, waitSeconds, position int) (*EscalationTier, error) {
	t := &EscalationTier{
		TierID:           NewID(),
		TenantID:         strings.TrimSpace(tenantID),
		PolicyID:         strings.TrimSpace(policyID),
		OnCallScheduleID: strings.TrimSpace(onCallScheduleID),
		WaitSeconds:      waitSeconds,
		Position:         position,
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return t, nil
}

// EscalationPolicyRepository administers escalation policies and their
// ordered tier ladders (E5.2b-1).
//
// escalation_policy and escalation_tier are TENANT-OWNED: both are in
// TenantOwnedTables and carry row-level security, so this repository takes no
// tenant argument anywhere — the bound connection already is the boundary
// (ADR-TENANCY-002), exactly like OnCallScheduleRepository. There is no
// privileged-pool counterpart of this type in this story: nothing here runs
// as a cross-tenant background worker — the escalation ENGINE that consumes
// this configuration to actually page and advance tiers is E5.2b-2, entirely
// separate and explicitly out of this story's scope.
type EscalationPolicyRepository interface {
	Create(ctx context.Context, p *EscalationPolicy) (*EscalationPolicy, error)
	Get(ctx context.Context, policyID string) (*EscalationPolicy, error)
	// List returns a page of the caller's policies, keyset-paginated over
	// policy_id.
	List(ctx context.Context, limit int, after string) ([]*EscalationPolicy, error)
	// Update changes one or more fields under optimistic locking. Returns
	// ErrVersionMismatch on a stale rowVersion, ErrNotFound if policyID does
	// not name a policy visible to the caller's tenant.
	Update(ctx context.Context, policyID string, rowVersion int64, patch EscalationPolicyPatch) (*EscalationPolicy, error)
	// Delete removes a policy; ON DELETE CASCADE removes its tiers with it (a
	// tier has no standalone meaning once the policy itself is gone).
	Delete(ctx context.Context, policyID string) error

	// AddTier appends a tier pointed at onCallScheduleID to policyID's ladder
	// at the next position (the current tier count — 0 for the first tier).
	// onCallScheduleID must already name a schedule belonging to the caller's
	// tenant — re-verified against this store's own tenant-scoped connection
	// before the row is written (ADR-ASSET-001 §6: a foreign key alone
	// bypasses row-level security on the referenced table via the FK
	// trigger's own privileges) — a cross-tenant or non-existent
	// onCallScheduleID returns ErrNotFound.
	AddTier(ctx context.Context, policyID, onCallScheduleID string, waitSeconds int) (*EscalationTier, error)
	// RemoveTier removes one tier and closes the resulting gap in Position
	// (every remaining tier after the removed one shifts down by one),
	// keeping positions contiguous 0..N-1. Returns ErrNotFound if tierID does
	// not name a tier of policyID.
	RemoveTier(ctx context.Context, policyID, tierID string) error
	// ListTiers returns a page of policyID's ladder in escalation order
	// (Position ascending), keyset-paginated over Position itself, the same
	// shape OnCallScheduleRepository.ListParticipants uses for its own
	// order-is-the-thing-being-paged roster.
	ListTiers(ctx context.Context, policyID string, limit int, after string) ([]*EscalationTier, error)
	// ReorderTiers atomically replaces policyID's ladder order: tierIDs must
	// be exactly the policy's current tier set, in the desired new order — no
	// more, no fewer, and every element already a tier of this policy. On
	// success every tier's Position is rewritten to its new 0-based rank and
	// Position stays contiguous 0..N-1 throughout. A non-matching set is
	// refused with a validation error before anything is written.
	ReorderTiers(ctx context.Context, policyID string, tierIDs []string) ([]*EscalationTier, error)
}
