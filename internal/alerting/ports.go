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
	// RecordTransition CAS-updates last_state/last_transition_at, fenced on
	// rowVersion — the same fencing shape CollectorCheckRepository.Update
	// uses, applied here so a rule edited or disabled between this tick's
	// read and its write does not have its firing state clobbered. Returns
	// domain.ErrVersionMismatch on a stale rowVersion, domain.ErrNotFound if
	// the rule no longer exists (deleted mid-tick).
	RecordTransition(ctx context.Context, ruleID string, rowVersion int64, state domain.AlertRuleState, at time.Time) (*domain.AlertRule, error)
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
