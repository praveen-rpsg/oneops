package domain

import (
	"strings"
	"time"
)

// EscalationStateStatus is the lifecycle of one incident's escalation
// work-queue row (E5.2b-2, ADR-ONCALL-003). Four states: active is the only
// non-terminal one — an incident is escalated as long as its row stays
// active, and paging stops the moment it becomes any of the other three.
type EscalationStateStatus string

const (
	// EscalationStateActive: the incident is still open and unacknowledged;
	// the Worker keeps paging its policy's ladder.
	EscalationStateActive EscalationStateStatus = "active"
	// EscalationStateAcked: the Worker observed IncidentAcknowledged on a
	// later cycle and stopped paging — an ack cancels escalation.
	EscalationStateAcked EscalationStateStatus = "acked"
	// EscalationStateResolved: the Worker observed some other non-open,
	// non-acknowledged incident status (investigating, resolved, closed,
	// reopened) and stopped paging. "Resolved" is the catch-all label for
	// that whole class, not only IncidentResolved itself — see the Worker's
	// own doc comment for the honest bound this simplification carries.
	EscalationStateResolved EscalationStateStatus = "resolved"
	// EscalationStateExhausted: every tier in the policy's ladder was
	// reached (paged, or skipped for a revoked/absent on-call user) and the
	// incident is still unacknowledged. Terminal — nothing pages it
	// further; an operator must notice by some other means.
	EscalationStateExhausted EscalationStateStatus = "exhausted"
)

// Valid reports whether s is a defined status.
func (s EscalationStateStatus) Valid() bool {
	switch s {
	case EscalationStateActive, EscalationStateAcked, EscalationStateResolved, EscalationStateExhausted:
		return true
	default:
		return false
	}
}

// Terminal reports whether s will never be claimed again.
func (s EscalationStateStatus) Terminal() bool { return s != EscalationStateActive }

// EscalationState is the per-incident escalation work-queue row (E5.2b-2):
// at most one per incident (the database's uq_escalation_state_tenant_incident,
// UNIQUE (tenant_id, incident_id)), tracking which tier of PolicyID's ladder
// the incident is currently at and when it is next due to be (re-)paged.
//
// TenantID is carried on the struct rather than resolved from a bound
// connection, for the identical reason domain.Notification's own doc comment
// gives: the Seeder and Worker that write and read this table run on the
// privileged, cross-tenant connection, so there is no bound tenant to read
// there. It is set exactly once, at seed time, from the escalated incident's
// own TenantID — never guessed, never client-supplied (ADR-TENANCY-003):
// there is no HTTP write path onto this table at all.
type EscalationState struct {
	StateID          string
	TenantID         string
	IncidentID       string
	PolicyID         string
	CurrentTierIndex int
	// NextAttemptAt is when this row is next due to be claimed and acted on
	// (paged/escalated, or found terminal). Set to "now" at seed time (page
	// the first tier immediately) and to "now + this tier's WaitSeconds"
	// every time the Worker advances past a tier.
	NextAttemptAt time.Time
	// ClaimedAt is the fencing token a Worker is handed by Store.ClaimDue and
	// must present back to Store.Advance/MarkTerminal (ADR-CONCURRENCY-005),
	// mirroring notification.Store's identical claim/fence contract. Zero
	// means unclaimed.
	ClaimedAt  time.Time
	Status     EscalationStateStatus
	Attempts   int
	RowVersion int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Validate enforces the invariants the database also enforces, so a bad
// value fails with a field-level error rather than a constraint-violation
// surfacing as an opaque 500 (this type has no HTTP surface today, but the
// discipline is the same one every other domain type in this codebase
// applies regardless).
func (s *EscalationState) Validate() error {
	if strings.TrimSpace(s.StateID) == "" {
		return newValidation("state_id", "must not be empty")
	}
	if strings.TrimSpace(s.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(s.IncidentID) == "" {
		return newValidation("incident_id", "must not be empty")
	}
	if strings.TrimSpace(s.PolicyID) == "" {
		return newValidation("policy_id", "must not be empty")
	}
	if s.CurrentTierIndex < 0 {
		return newValidation("current_tier_index", "must not be negative")
	}
	if !s.Status.Valid() {
		return newValidation("status", "must be one of: active, acked, resolved, exhausted")
	}
	if s.Attempts < 0 {
		return newValidation("attempts", "must not be negative")
	}
	return nil
}

// NewEscalationState builds a freshly seeded, active state at tier 0, due
// immediately — the Seeder's only constructor.
func NewEscalationState(tenantID, incidentID, policyID string) (*EscalationState, error) {
	now := time.Now().UTC()
	s := &EscalationState{
		StateID:          NewID(),
		TenantID:         strings.TrimSpace(tenantID),
		IncidentID:       strings.TrimSpace(incidentID),
		PolicyID:         strings.TrimSpace(policyID),
		CurrentTierIndex: 0,
		NextAttemptAt:    now,
		Status:           EscalationStateActive,
		Attempts:         0,
		RowVersion:       1,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}
