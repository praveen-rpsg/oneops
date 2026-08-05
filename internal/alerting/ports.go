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
//
// A candidate transition must additionally hold continuously for the rule's
// own FlapDwellSeconds before it is committed (E3.2's de-flap hysteresis,
// ADR-ALERTING-001) — see Evaluator.dwellSatisfied's doc comment. This too is
// a property of how a firing is derived, not a new stored/reified kind of
// thing: the in-flight candidate it tracks (domain.AlertRule.PendingState/
// PendingSince) is bookkeeping on the same config row, not a second entity.
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
	// comment for why the two travel together. pending_state/pending_since
	// (E3.2) are cleared in the same statement too: a committed transition
	// supersedes whatever candidate the dwell was timing. Returns
	// domain.ErrVersionMismatch on a stale rowVersion, domain.ErrNotFound if
	// the rule no longer exists (deleted mid-tick).
	RecordTransition(ctx context.Context, ruleID string, rowVersion int64, state domain.AlertRuleState, at time.Time, currentIncidentID *string) (*domain.AlertRule, error)
	// RecordPending CAS-updates pending_state/pending_since only, fenced on
	// rowVersion exactly like RecordTransition — E3.2's de-flap dwell
	// bookkeeping (Evaluator.dwellSatisfied's doc comment). pendingState ==
	// nil (with pendingSince also nil) clears both: the metric reverted to
	// LastState before its own dwell completed. Persisting this rather than
	// holding it only in the evaluator's memory is what lets the dwell clock
	// survive a process restart or leader failover — the next tick, by this
	// leader or a new one, re-reads it off the row. Returns the same errors
	// as RecordTransition for the same reasons.
	RecordPending(ctx context.Context, ruleID string, rowVersion int64, pendingState *domain.AlertRuleState, pendingSince *time.Time) (*domain.AlertRule, error)
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

// MaintenanceWindowChecker is E3.3a's privileged read+record port
// (ADR-ALERTING-002): whether (tenantID, assetID) is inside an ACTIVE
// maintenance window at `at`, and, when it is, recording that a firing was
// suppressed because of it. *postgres.MaintenanceWindowStore satisfies it
// when built over the PRIVILEGED pool — the same dual-role split Store and
// IncidentCorrelator above already document; the admin CRUD API
// (domain.MaintenanceWindowRepository) is built from the tenant-scoped pool
// instead.
//
// tenantID is always sourced from the firing rule's own row (rule.TenantID),
// never assumed or cached across rules — the identical non-decision
// ADR-TENANCY-012 requires of QueryRangeForTenant and IncidentCorrelator's
// methods, applied here a third time: this connection has row-level
// security switched off.
type MaintenanceWindowChecker interface {
	// Suppress reports whether an ACTIVE window (at ∈ [starts_at, ends_at),
	// half-open) covers (tenantID, assetID) at `at` and, if one does,
	// atomically records the suppression on it (suppressed_count++,
	// last_suppressed_at = at) in the SAME operation — see
	// postgres.MaintenanceWindowStore.Suppress's doc comment for why a
	// check-then-act race between two rules firing on the same asset at once
	// must not undercount. windowID is meaningful only when suppressed is
	// true (used for logging, not for any further decision the evaluator
	// makes).
	Suppress(ctx context.Context, tenantID, assetID string, at time.Time) (windowID string, suppressed bool, err error)
}

// DependencySuppressionChecker is E3.3b's privileged read+record port
// (ADR-ALERTING-003): whether assetID (tenantID) transitively depends — via
// depends_on/runs_on edges only, up to an implementation-chosen bounded
// depth — on some asset that is currently "down" (has its own enabled,
// firing alert_rule), and, when it does, recording that a firing was
// suppressed because of it, naming the root. *postgres.
// DependencySuppressionStore satisfies it when built over the PRIVILEGED
// pool for its down-check/recording AND handed a domain.TypedGraphTraversal
// built over the TENANT-SCOPED pool for the dependency walk — see that
// type's own doc comment for why the walk must stay RLS-scoped even though
// the down-check is privileged and cross-tenant (ADR-TENANCY-001).
//
// tenantID is always sourced from the firing rule's own row (rule.TenantID),
// never assumed or cached across rules — the identical non-decision
// ADR-TENANCY-012 already requires of MaintenanceWindowChecker,
// IncidentCorrelator and TelemetryReader, applied here a fourth time.
type DependencySuppressionChecker interface {
	// Suppress reports whether assetID's dependency closure contains a
	// currently-down asset for tenantID and, if so, atomically records the
	// suppression against every down root found (suppressed_count++,
	// last_suppressed_at = at), naming rootAssetID as the shallowest,
	// then lexicographically-first, one — meaningful only when suppressed
	// is true (used for logging, not for any further decision the
	// evaluator makes, the same convention MaintenanceWindowChecker.
	// Suppress's windowID follows).
	Suppress(ctx context.Context, tenantID, assetID string, at time.Time) (rootAssetID string, suppressed bool, err error)
}

// NopDependencySuppressionChecker is the "no dependency-suppression
// capability configured" stand-in NewEvaluator's doc comment requires: it
// always reports no down dependency, so a deployment with no CMDB-backed
// checker wired gets E3.1/E3.2/E3.3a/E4.1's exact pre-E3.3b behaviour rather
// than a nil-pointer panic. Unlike IncidentCorrelator,
// DependencySuppressionChecker has no nil-means-skip convention — the same
// reason MaintenanceWindowChecker does not — so this exists to be passed
// explicitly instead.
type NopDependencySuppressionChecker struct{}

// Suppress implements DependencySuppressionChecker: never suppresses.
func (NopDependencySuppressionChecker) Suppress(context.Context, string, string, time.Time) (string, bool, error) {
	return "", false, nil
}

// NopMaintenanceWindowChecker is the "no maintenance-window capability
// configured" stand-in NewEvaluator's doc comment requires: it always reports
// no active window, so a deployment with no maintenance_window store wired
// gets E3.1/E4.1's exact pre-E3.3a behaviour rather than a nil-pointer panic.
// Unlike IncidentCorrelator, MaintenanceWindowChecker has no nil-means-skip
// convention — see NewEvaluator's doc comment for why — so this exists to be
// passed explicitly instead.
type NopMaintenanceWindowChecker struct{}

// Suppress implements MaintenanceWindowChecker: never suppresses.
func (NopMaintenanceWindowChecker) Suppress(context.Context, string, string, time.Time) (string, bool, error) {
	return "", false, nil
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
	// IncSuppressed counts an ok->firing transition suppressed by an active
	// maintenance window (E3.3a) — distinct from IncErrors: a suppression is
	// the maintenance-window check working as designed, not a failure.
	IncSuppressed()
	// IncDependencySuppressed counts an ok->firing transition suppressed
	// because the firing asset transitively depends on a currently-down
	// asset (E3.3b) — a separate reason from IncSuppressed, so an operator
	// reading these two counters can tell "planned maintenance" apart from
	// "collateral of a dependency outage" without grepping logs.
	IncDependencySuppressed()
	IncErrors()
}

// NopMetrics is the no-op Metrics.
type NopMetrics struct{}

func (NopMetrics) IncEvaluated()            {}
func (NopMetrics) IncFired()                {}
func (NopMetrics) IncRecovered()            {}
func (NopMetrics) IncSuppressed()           {}
func (NopMetrics) IncDependencySuppressed() {}
func (NopMetrics) IncErrors()               {}
