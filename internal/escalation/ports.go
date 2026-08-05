// Package escalation is E5.2b-2's leader-gated escalation engine
// (ADR-ONCALL-003): it pages an incident's on-call chain and escalates
// through its escalation_policy's ordered tiers while the incident stays
// open and unacknowledged, stopping the moment it is acknowledged or
// resolved.
//
// Two SEPARATE leader-gated passes, mirroring internal/notification's
// Worker and internal/grouping's Reconciler shapes exactly rather than
// inventing a third:
//
//   - Seeder enrols newly-open, unacknowledged, alert-sourced incidents into
//     escalation_state at the tenant's default policy. It is DECOUPLED from
//     internal/alerting's evaluator: it never reads or writes anything the
//     hot path touches, and runs as its own periodic sweep — the same
//     "separate reconciliation pass, not a hook inside the hot path" choice
//     internal/grouping's own package doc comment makes for E4.2.
//   - Worker claims due escalation_state rows and, for each: re-checks the
//     incident is still open (an ack or resolve cancels escalation on the
//     very next cycle — see Worker.process's own doc comment); resolves
//     who is on call for the current tier's schedule; re-verifies that
//     person is still an ACTIVE member of the tenant before paging them
//     (the page-time re-check E5.2a's own review flagged as a requirement,
//     ADR-ONCALL-003 §4); pages by enqueuing an email
//     domain.Notification (no change to internal/notification); and
//     advances to the next tier or marks the row exhausted.
//
// escalation_state (the work-queue) is privileged and cross-tenant, the
// identical dual-role split every other cross-tenant background worker in
// this platform draws (notification.Store, grouping.Store): this package's
// Store port is satisfied by *postgres.EscalationStateStore, built over the
// PRIVILEGED pool. Every OTHER read the Worker performs — the incident, the
// policy's tiers, who is on call, active membership, the on-call user's
// email — reuses the EXISTING tenant-scoped repositories this platform
// already built for E5.1/E5.2a/E5.2b-1 unchanged, bound to the claimed
// state's own tenant per row (domain.WithTenant) rather than a privileged
// re-implementation of any of them. That tenant value is never trusted
// blindly: it was written into escalation_state exactly once, at seed time,
// by Store.Seed itself, from the SAME query's own join of incident to
// escalation_policy on tenant_id — never from a client, and never
// re-derived from anywhere else afterward (ADR-TENANCY-003).
package escalation

import (
	"context"
	"errors"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// Store is the escalation_state work-queue port. *postgres.EscalationStateStore
// satisfies it, built over the PRIVILEGED pool: both Seeder and Worker
// process every tenant from one process, like notification.Store/
// grouping.Store.
type Store interface {
	// Seed enrols every open, unacknowledged, alert-sourced incident with no
	// escalation_state row yet, across every tenant, at the tenant's default
	// policy — the tenant's own single active escalation_policy, or, when a
	// tenant has declared more than one, the earliest by created_at
	// (ADR-ONCALL-003 §2; per-asset/per-service routing is explicitly
	// deferred). An incident whose tenant has no active policy is left
	// unseeded — not an error. Returns how many rows were seeded and how
	// many eligible incidents were skipped for lacking an active policy.
	Seed(ctx context.Context, now time.Time) (seeded, skippedNoPolicy int, err error)

	// ClaimDue atomically claims active, due escalation_state rows across
	// every tenant under FOR UPDATE SKIP LOCKED, stamping claimed_at as the
	// fencing token and charging the attempt — the identical shape
	// notification.Store.ClaimDue uses (ADR-CONCURRENCY-002/005/006/007).
	ClaimDue(ctx context.Context, now time.Time, lease time.Duration, limit int) ([]*domain.EscalationState, error)
	// Advance moves a claimed row to nextTierIndex, due again at
	// nextAttemptAt, fenced on claimToken. Returns ErrStaleClaim if the row
	// was reclaimed since ClaimDue (ADR-CONCURRENCY-005).
	Advance(ctx context.Context, stateID string, claimToken time.Time, nextTierIndex int, nextAttemptAt time.Time) error
	// MarkTerminal moves a claimed row to a terminal status (acked, resolved,
	// or exhausted — never active), fenced on claimToken. Returns
	// ErrStaleClaim if the row was reclaimed since ClaimDue.
	MarkTerminal(ctx context.Context, stateID string, claimToken time.Time, status domain.EscalationStateStatus) error
	// ReleaseClaim returns an unused claim and gives back the attempt it
	// charged, for a Worker stopped between claiming and processing
	// (demotion, shutdown) — the honest counterpart to ClaimDue, mirroring
	// notification.Store.ReleaseClaim exactly.
	ReleaseClaim(ctx context.Context, stateID string, claimToken time.Time) error
}

// ErrStaleClaim mirrors notification.ErrStaleClaim exactly, for the
// identical reason (ADR-CONCURRENCY-005): a fenced write that lost the race
// is not a failure, it is the current claim owner's outcome to record
// instead.
var ErrStaleClaim = errors.New("escalation: result fenced — row reclaimed by another worker")

// IncidentReader is the narrow read the Worker needs to decide whether an
// incident is still open and unacknowledged. *postgres.IncidentStore (built
// over the TENANT-SCOPED pool, the same instance srv.SetIncidents wires)
// satisfies it.
type IncidentReader interface {
	Get(ctx context.Context, incidentID string) (*domain.Incident, error)
}

// PolicyReader is the narrow read the Worker needs to walk a policy's tier
// ladder. *postgres.EscalationPolicyStore (tenant-scoped, E5.2b-1) satisfies
// it.
type PolicyReader interface {
	ListTiers(ctx context.Context, policyID string, limit int, after string) ([]*domain.EscalationTier, error)
}

// OnCallResolver resolves who is on call for a schedule at a given instant.
// *postgres.OnCallScheduleStore (tenant-scoped, E5.2a) satisfies it.
type OnCallResolver interface {
	OnCall(ctx context.Context, scheduleID string, at time.Time) (*domain.OnCallResolution, error)
}

// MembershipChecker confirms a user is a person the tenant still actually
// recognises via an ACTIVE membership row — the page-time re-check
// (ADR-ONCALL-003 §4). *postgres.MembershipStore (tenant-scoped) satisfies
// it via its ActiveMember method.
type MembershipChecker interface {
	ActiveMember(ctx context.Context, userID string) (bool, error)
}

// UserReader resolves a user's email for paging. *postgres.UserStore
// satisfies it; app_user is a GLOBAL table with no row-level security
// (ADR-IDENTITY-002 §3.1), so this read needs no tenant scoping of its own —
// see OnCallScheduleStore.OnCall's own doc comment for the identical
// reasoning applied to display_name.
type UserReader interface {
	Get(ctx context.Context, userID string) (*domain.User, error)
}

// Notifier enqueues the page as a tracked domain.Notification — the
// Worker's only outbound side effect besides its own bookkeeping.
// *notification.Service satisfies it; this package makes no other call into
// internal/notification and changes nothing there.
type Notifier interface {
	Enqueue(ctx context.Context, n *domain.Notification) (*domain.Notification, error)
}

// Metrics receives Seeder/Worker observability signals (§3/§5). NopMetrics
// discards them.
type Metrics interface {
	// IncSeeded records n newly-seeded escalation_state rows in one Seeder
	// pass (n may be 0).
	IncSeeded(n int)
	// IncSkippedNoPolicy records n eligible incidents left unseeded because
	// their tenant has no active escalation policy (n may be 0).
	IncSkippedNoPolicy(n int)
	// IncPaged counts one tier actually paged (a Notification enqueued).
	IncPaged()
	// IncEscalated counts one tier-index advance, whether or not it paged —
	// a skipped/revoked tier still moves the ladder forward.
	IncEscalated()
	// IncSkippedRevoked counts one tier NOT paged because there was no
	// on-call user, or the resolved user is not an active tenant member.
	IncSkippedRevoked()
	// IncExhausted counts one escalation_state row that reached the end of
	// its policy's ladder still unacknowledged.
	IncAcked()
	IncResolved()
	IncExhausted()
	IncErrors()
}

// NopMetrics is the no-op Metrics.
type NopMetrics struct{}

func (NopMetrics) IncSeeded(int)          {}
func (NopMetrics) IncSkippedNoPolicy(int) {}
func (NopMetrics) IncPaged()              {}
func (NopMetrics) IncEscalated()          {}
func (NopMetrics) IncSkippedRevoked()     {}
func (NopMetrics) IncAcked()              {}
func (NopMetrics) IncResolved()           {}
func (NopMetrics) IncExhausted()          {}
func (NopMetrics) IncErrors()             {}
