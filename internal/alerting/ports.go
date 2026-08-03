// Package alerting is the leader-gated alert-rule evaluator (E3.1): it derives
// a firing by evaluating each enabled domain.AlertRule against telemetry and,
// on a state TRANSITION only (ok->firing or firing->ok), enqueues exactly one
// domain.Notification. It mirrors internal/collector's scheduler and
// internal/store/postgres's telemetry rollup worker shape — a leader-gated
// sweep over the privileged pool, serving every tenant from one process.
//
// There is deliberately no reified Alert/Event/Signal type anywhere in this
// package (docs/PLATFORM-BUILD-PLAN.md §4, Vol II §5.3): a firing is a
// decision this package makes each tick, not a row it stores. What IS stored
// is the rule (config) and the Notification a transition produces.
package alerting

import (
	"context"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// Store is the persistence port the Evaluator depends on.
// *postgres.AlertRuleStore satisfies it when built over the PRIVILEGED pool,
// exactly as CollectorCheckStore satisfies collector.Store over that same
// pool for the scheduler's due-scan — the evaluator serves every tenant's
// enabled rules from one process, while the admin CRUD API is built from the
// tenant-scoped pool (domain.AlertRuleRepository) so one tenant cannot see or
// change another's rules.
type Store interface {
	// EnabledRules returns up to limit enabled rules, across every tenant,
	// keyset-paginated over rule_id — the same shape List's pagination takes,
	// applied without a tenant filter because this is the privileged,
	// cross-tenant read.
	EnabledRules(ctx context.Context, limit int, after string) ([]*domain.AlertRule, error)
	// RecordTransition CAS-updates last_state/last_transition_at/
	// current_incident_id, fenced on rowVersion — the same fencing shape
	// CollectorCheckRepository.Update uses, applied here so a rule edited or
	// disabled between this tick's read and its write does not have its
	// firing state clobbered. currentIncidentID is written verbatim (nil
	// clears the link, a non-nil value sets it) in the SAME statement as the
	// state transition, not a second write — see Evaluator.correlate's doc
	// comment for why the two travel together. Returns
	// domain.ErrVersionMismatch on a stale rowVersion, domain.ErrNotFound if
	// the rule no longer exists (deleted mid-tick).
	RecordTransition(ctx context.Context, ruleID string, rowVersion int64, state domain.AlertRuleState, at time.Time, currentIncidentID *string) (*domain.AlertRule, error)
}

// IncidentCorrelator is E4.1's create-or-link port: wires an alert rule's
// ok->firing/firing->ok transitions into Incident (E5.1) creation/linking.
// *postgres.IncidentStore satisfies it when built over the PRIVILEGED pool —
// the same dual-role split Store above already documents — while the admin
// CRUD API (domain.IncidentRepository) is built from the tenant-scoped pool.
//
// Every method is given tenantID explicitly (either directly, or via
// want.TenantID) and carries it as a SQL predicate in its implementation:
// this pool has row-level security switched off, so nothing else confines a
// correlation read/write to one tenant (ADR-TENANCY-012). The Evaluator
// always sources tenantID from the firing rule's own row, never a value
// guessed or cached across rules — the same non-decision ADR-TENANCY-012
// requires of QueryRangeForTenant's tenantID.
//
// The interface is deliberately narrow: it has no method that can change an
// Incident's Status. E4.1's scope is correlate, not remediate — an operator
// closes an incident, never the evaluator — and the absence of a status
// method here makes that a compile-time guarantee, not merely a documented
// intention.
type IncidentCorrelator interface {
	// FindOrCreateOpenAlertIncident returns the id of want.TenantID's OPEN
	// (status not resolved or closed), alert-sourced incident already linked
	// to want.AssetID — appending one alert_note timeline row carrying
	// noteOnLink to it — or inserts want itself (source is asserted to
	// already be domain.IncidentSourceAlert; Validate is re-run) when none
	// exists, recording the usual IncidentEventCreated row instead. actor
	// names the writer for either timeline row (the evaluator's own system
	// identity, never a request's ActorFrom(ctx)).
	//
	// Atomicity is a database-level constraint, not an application lock:
	// see postgres.IncidentStore.FindOrCreateOpenAlertIncident's doc comment
	// for how two rules firing on the same (tenant, asset) concurrently, from
	// different evaluator goroutines, can never both create a row (E4.1's
	// no-duplicate-incident requirement).
	FindOrCreateOpenAlertIncident(ctx context.Context, want *domain.Incident, actor, noteOnLink string) (incidentID string, err error)
	// AppendAlertNote appends one alert_note timeline row to incidentID,
	// first re-verifying — under an explicit tenant_id predicate — that
	// incidentID names a row owned by tenantID. Returns domain.ErrNotFound
	// otherwise: a rule's CurrentIncidentID is never trusted to still belong
	// to its own tenant without this check (ADR-TENANCY-012's cross-tenant
	// defense for this correlation write).
	AppendAlertNote(ctx context.Context, tenantID, incidentID, note, actor string) error
}

// TelemetryReader is the narrow, tenant-explicit telemetry read the evaluator
// needs — see domain.TelemetryRepository.QueryRangeForTenant's doc comment
// for why QueryRange itself (RLS-only isolation) is unsafe to call from this
// package's privileged connection.
type TelemetryReader interface {
	QueryRangeForTenant(ctx context.Context, tenantID, assetID, metric string, from, to time.Time) ([]domain.Sample, error)
}

// Notifier is the narrow slice of notification.Service the evaluator needs.
// *notification.Service satisfies it directly (same method signature), the
// same way *notification.Service already backs policy.Notifier via
// notification.PolicyNotifier for a privileged producer.
type Notifier interface {
	Enqueue(ctx context.Context, n *domain.Notification) (*domain.Notification, error)
}

// Metrics receives evaluator observability signals. NopMetrics discards them.
type Metrics interface {
	IncEvaluated()
	IncFired()
	IncRecovered()
	IncErrors()
}

// NopMetrics is the no-op Metrics.
type NopMetrics struct{}

func (NopMetrics) IncEvaluated() {}
func (NopMetrics) IncFired()     {}
func (NopMetrics) IncRecovered() {}
func (NopMetrics) IncErrors()    {}
