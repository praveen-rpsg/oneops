package domain

import (
	"context"
	"strings"
	"time"
)

// OnCallScheduleStatus is the lifecycle of an on-call schedule. Modelled on
// TeamStatus, not on a richer state machine: a schedule is a governed,
// named object an operator archives rather than deletes so anything already
// pointing at it (a participant row, a future E5.2b escalation tier) is not
// orphaned (ADR-IDENTITY-001 §8.3's reasoning, applied here).
type OnCallScheduleStatus string

const (
	OnCallScheduleActive   OnCallScheduleStatus = "active"
	OnCallScheduleArchived OnCallScheduleStatus = "archived"
)

// Valid reports whether s is a defined status.
func (s OnCallScheduleStatus) Valid() bool {
	return s == OnCallScheduleActive || s == OnCallScheduleArchived
}

// MaxOnCallScheduleNameLength bounds a free-text field that reaches consoles
// and logs, the same bound Team.Name carries.
const MaxOnCallScheduleNameLength = 200

// MinOnCallHandoffIntervalSeconds is the floor on how often a rotation hands
// off: zero or negative would make the rotation math (OnCallRotationIndex)
// divide by a non-positive duration, which is nonsensical rather than merely
// unusual, so it is refused outright rather than clamped.
const MinOnCallHandoffIntervalSeconds = 1

// OnCallSchedule is a named on-call rotation: a set of participants
// (OnCallParticipant, ordered by Position) who hand the on-call duty to one
// another every HandoffIntervalSeconds, starting at RotationStartAt (E5.2a).
//
// This is a real, first-class operational object an operator declares — not
// one of the reduced false-nouns (docs/PLATFORM-BUILD-PLAN.md §4, Vol II
// §5.3), exactly the same reasoning MaintenanceWindow's own doc comment
// applies. What IS reduced-concept-sensitive is "who is on call right now":
// that is never a stored fact on this type or on any row anywhere — it is
// always recomputed from RotationStartAt/HandoffIntervalSeconds and the
// current ordered participant list by OnCallRotationIndex, a pure function.
// A per-shift assignment table (row per person per period) was the framework
// this story explicitly rejects: it would need a background job to keep
// generating future shifts, a query to find "the" current one, and a second
// source of truth that could drift from the rotation's own definition.
type OnCallSchedule struct {
	ScheduleID string
	TenantID   string
	Name       string
	// HandoffIntervalSeconds is how long each participant holds on-call duty
	// before the rotation advances to the next one, in whole seconds. All
	// rotation math is seconds-based UTC arithmetic — no calendars, no time
	// zones, no DST — a deliberate simplification (see OnCallRotationIndex's
	// doc comment); a schedule that needs "every Monday 09:00 local time"
	// semantics is out of this story's scope.
	HandoffIntervalSeconds int
	// RotationStartAt anchors the rotation: at exactly this instant (and
	// every HandoffIntervalSeconds after it) the on-call duty hands to the
	// next participant in Position order. Stored and compared in UTC.
	RotationStartAt time.Time
	Status          OnCallScheduleStatus
	RowVersion      int64
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Active reports whether the schedule is in ordinary use.
func (s *OnCallSchedule) Active() bool { return s.Status == OnCallScheduleActive }

// Validate enforces the invariants the database also enforces, so a bad
// request fails with a field-level 422 rather than a constraint-violation 500.
func (s *OnCallSchedule) Validate() error {
	if strings.TrimSpace(s.ScheduleID) == "" {
		return newValidation("schedule_id", "must not be empty")
	}
	if strings.TrimSpace(s.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	name := strings.TrimSpace(s.Name)
	if name == "" {
		return newValidation("name", "must not be empty")
	}
	if len(name) > MaxOnCallScheduleNameLength {
		return newValidation("name", "must be at most 200 characters")
	}
	if s.HandoffIntervalSeconds < MinOnCallHandoffIntervalSeconds {
		return newValidation("handoff_interval_seconds", "must be greater than 0")
	}
	if s.RotationStartAt.IsZero() {
		return newValidation("rotation_start_at", "must not be zero")
	}
	if !s.Status.Valid() {
		return newValidation("status", "must be one of: active, archived")
	}
	return nil
}

// NewOnCallSchedule builds a freshly minted, active schedule.
func NewOnCallSchedule(
	tenantID, name string, handoffIntervalSeconds int, rotationStartAt time.Time,
) (*OnCallSchedule, error) {
	s := &OnCallSchedule{
		ScheduleID:             NewID(),
		TenantID:               strings.TrimSpace(tenantID),
		Name:                   strings.TrimSpace(name),
		HandoffIntervalSeconds: handoffIntervalSeconds,
		RotationStartAt:        rotationStartAt.UTC(),
		Status:                 OnCallScheduleActive,
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// OnCallSchedulePatch changes one or more fields of a schedule under
// optimistic locking. Every field is a pointer so an absent field is
// distinguishable from an explicit zero value — the same shape
// AlertRulePatch uses.
type OnCallSchedulePatch struct {
	Name                   *string
	HandoffIntervalSeconds *int
	RotationStartAt        *time.Time
	Status                 *OnCallScheduleStatus
}

// OnCallParticipant is one person's seat in a schedule's rotation order.
// Position is 0-based and contiguous within a schedule (0..N-1): AddParticipant
// appends at N, RemoveParticipant closes the resulting gap, and
// ReorderParticipants rewrites the whole sequence atomically — see
// OnCallScheduleRepository's doc comment on each. It is a durable roster
// entry, never a per-shift/per-period row: there is exactly one row per
// (schedule, participant) regardless of how many times the rotation has
// cycled past them.
type OnCallParticipant struct {
	ParticipantID string
	TenantID      string
	ScheduleID    string
	// UserID names a person the caller's tenant actually knows — re-verified
	// against an ACTIVE membership row before this row is written
	// (OnCallScheduleRepository.AddParticipant's doc comment), the identical
	// defense IncidentStore.verifyAssigneeExists applies to Incident's
	// assignee (ADR-IDENTITY-002 §3.1: app_user itself carries no tenant_id).
	UserID     string
	Position   int
	RowVersion int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Validate enforces the invariants the database also enforces.
func (p *OnCallParticipant) Validate() error {
	if strings.TrimSpace(p.ParticipantID) == "" {
		return newValidation("participant_id", "must not be empty")
	}
	if strings.TrimSpace(p.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(p.ScheduleID) == "" {
		return newValidation("schedule_id", "must not be empty")
	}
	if strings.TrimSpace(p.UserID) == "" {
		return newValidation("user_id", "must not be empty")
	}
	if p.Position < 0 {
		return newValidation("position", "must not be negative")
	}
	return nil
}

// NewOnCallParticipant builds a participant at the given position. The
// repository, not this constructor, decides what that position is (append at
// the current count) — see OnCallScheduleRepository.AddParticipant.
func NewOnCallParticipant(scheduleID, tenantID, userID string, position int) (*OnCallParticipant, error) {
	p := &OnCallParticipant{
		ParticipantID: NewID(),
		TenantID:      strings.TrimSpace(tenantID),
		ScheduleID:    strings.TrimSpace(scheduleID),
		UserID:        strings.TrimSpace(userID),
		Position:      position,
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// OnCallRotationIndex answers "which of participantCount participants (by
// their 0-based rank in Position order — NOT the stored Position value
// itself, which need not be contiguous the instant this is called) holds the
// on-call seat at `at`" for a rotation beginning at rotationStartAt and
// handing off every handoffIntervalSeconds. This is the whole of E5.2a's
// "who's on call" logic: deterministic, pure, and independent of any store —
// no per-shift row is ever consulted or produced.
//
// ok is false only when participantCount <= 0: nobody is on call, which is a
// valid, ordinary state (an empty rotation), never an error.
//
// at precedes rotationStartAt: the rotation has not started, so the first
// participant (index 0) holds the seat — the same "not yet begun" convention
// a not-yet-active resource defaults to its initial state under.
//
// Otherwise: elapsed = at - rotationStartAt; steps = floor(elapsed /
// handoffIntervalSeconds); index = steps mod participantCount, using a
// POSITIVE modulo (Go's % can return a negative result for a negative
// dividend — steps is never negative here since at >= rotationStartAt on
// this branch, but the positive-modulo form is applied unconditionally so
// the function stays correct if that invariant ever changes upstream).
//
// The interval is HALF-OPEN, [rotationStartAt + k*interval,
// rotationStartAt + (k+1)*interval): at exactly a handoff boundary the
// rotation has already advanced — steps divides evenly and the NEXT
// participant holds the seat — mirroring the half-open convention
// MaintenanceWindow's [starts_at, ends_at) and TelemetryRepository.QueryRange's
// [from, to) already use in this codebase, so a boundary instant has exactly
// one owner everywhere it appears.
//
// All arithmetic is UTC and seconds-based: no calendar, no timezone, no DST.
// A rotation that needs "hand off every Monday at 09:00 local time" is out of
// this story's scope — see docs/adr/ADR-ONCALL-001's Decision on this point.
func OnCallRotationIndex(
	participantCount int, rotationStartAt time.Time, handoffIntervalSeconds int, at time.Time,
) (index int, ok bool) {
	if participantCount <= 0 {
		return 0, false
	}
	if handoffIntervalSeconds <= 0 {
		// Defensive only: Validate refuses to persist a non-positive interval,
		// so a live schedule never reaches this branch. Guarding it here
		// avoids a division-by-zero panic (time.Duration is int64; dividing
		// by a zero Duration panics) rather than trusting every caller to
		// have validated first.
		return 0, true
	}
	if at.Before(rotationStartAt) {
		return 0, true
	}

	elapsed := at.Sub(rotationStartAt)
	interval := time.Duration(handoffIntervalSeconds) * time.Second
	steps := int64(elapsed / interval)
	n := int64(participantCount)
	return int(positiveModulo(steps, n)), true
}

// positiveModulo returns x mod n in [0, n) — unlike Go's own %, which returns
// a negative result for a negative dividend (e.g. -1 % 3 == -1, not 2).
//
// Extracted to its own function, rather than inlined in OnCallRotationIndex,
// so its correctness is directly unit-testable with a negative x: the
// `at.Before(rotationStartAt)` branch above keeps every live caller's steps
// non-negative today, which would otherwise make a mutation of this formula
// (e.g. dropping back to a bare `x % n`) pass every OnCallRotationIndex test
// unchanged — a defensive guard that nothing in the public function's own
// test suite could prove is load-bearing. Testing this helper directly with
// negative x closes that gap.
func positiveModulo(x, n int64) int64 {
	return ((x % n) + n) % n
}

// OnCallScheduleRepository administers on-call schedules, their participant
// rosters, and the "who's on call now" computation (E5.2a).
//
// on_call_schedule and on_call_participant are TENANT-OWNED: both are in
// TenantOwnedTables and carry row-level security, so this repository takes no
// tenant argument anywhere — the bound connection already is the boundary
// (ADR-TENANCY-002), exactly like TeamRepository. There is no privileged-pool
// counterpart of this type: unlike AlertRuleRepository/MaintenanceWindowRepository,
// nothing in E5.2a runs as a cross-tenant background worker — "who's on call"
// is always answered on behalf of exactly one already-known tenant, at
// request time (ADR-ONCALL-001's Decision on this point).
type OnCallScheduleRepository interface {
	Create(ctx context.Context, s *OnCallSchedule) (*OnCallSchedule, error)
	Get(ctx context.Context, scheduleID string) (*OnCallSchedule, error)
	// List returns a page of the caller's schedules, keyset-paginated over
	// schedule_id.
	List(ctx context.Context, limit int, after string) ([]*OnCallSchedule, error)
	// Update changes one or more fields under optimistic locking. Returns
	// ErrVersionMismatch on a stale rowVersion, ErrNotFound if scheduleID does
	// not name a schedule visible to the caller's tenant.
	Update(ctx context.Context, scheduleID string, rowVersion int64, patch OnCallSchedulePatch) (*OnCallSchedule, error)
	// Delete removes a schedule; ON DELETE CASCADE removes its participants
	// with it (a rotation's roster has no standalone meaning once the
	// rotation itself is gone).
	Delete(ctx context.Context, scheduleID string) error

	// AddParticipant appends userID to scheduleID's roster at the next
	// position (the current participant count — 0 for the first participant).
	// userID must already be an ACTIVE member of the caller's tenant —
	// re-verified against this store's own tenant-scoped connection before
	// the row is written, mirroring IncidentStore.verifyAssigneeExists
	// exactly — a user who is not an active member returns ErrNotFound.
	// Adding a user already on this schedule returns ErrConflict: unlike
	// TeamMembership (unordered, so a duplicate add is a harmless no-op),
	// a participant carries a position, and silently ignoring a second add
	// would leave it ambiguous whether the caller also meant to reorder.
	AddParticipant(ctx context.Context, scheduleID, userID string) (*OnCallParticipant, error)
	// RemoveParticipant removes one participant and closes the resulting gap
	// in Position (every remaining participant after the removed one shifts
	// down by one), keeping positions contiguous 0..N-1. Returns ErrNotFound
	// if participantID does not name a participant of scheduleID.
	RemoveParticipant(ctx context.Context, scheduleID, participantID string) error
	// ListParticipants returns a page of scheduleID's roster in rotation
	// order (Position ascending), keyset-paginated over Position itself
	// (after is the last Position seen, "" meaning from the start) rather
	// than over ParticipantID — the rotation's own order IS the thing being
	// paged, unlike every other list in this codebase whose keyset happens to
	// coincide with insertion order.
	ListParticipants(ctx context.Context, scheduleID string, limit int, after string) ([]*OnCallParticipant, error)
	// ReorderParticipants atomically replaces scheduleID's rotation order:
	// participantIDs must be exactly the schedule's current participant set,
	// in the desired new order — no more, no fewer, and every element already
	// a participant of this schedule. On success every participant's Position
	// is rewritten to its new 0-based rank and Position stays contiguous
	// 0..N-1 throughout. A non-matching set is refused with a validation
	// error before anything is written.
	ReorderParticipants(ctx context.Context, scheduleID string, participantIDs []string) ([]*OnCallParticipant, error)

	// OnCall answers "who is on call for scheduleID at `at`" by applying
	// OnCallRotationIndex to the schedule's own RotationStartAt/
	// HandoffIntervalSeconds and its current ordered participant list. It is
	// a read, not a stored fact: nothing is written by calling this. Returns
	// ErrNotFound if scheduleID does not name a schedule visible to the
	// caller's tenant; an empty roster is NOT an error — the returned
	// OnCallResolution has a nil UserID.
	OnCall(ctx context.Context, scheduleID string, at time.Time) (*OnCallResolution, error)
}

// OnCallResolution is the answer to "who is on call for a schedule right
// now (or at some other instant)". UserID/DisplayName are both nil when the
// schedule has no participants — an ordinary, non-error state.
type OnCallResolution struct {
	ScheduleID  string
	At          time.Time
	UserID      *string
	DisplayName *string
}
