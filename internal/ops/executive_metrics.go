package ops

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/rpsg/oneops/internal/audit"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/governance"
)

// ExecutiveMetrics holds platform-wide operational collectors that complement the
// existing HTTP metrics (observability) and audit-verification metrics
// (PromMetrics). It records only what the platform layer observes; it adds no
// governance or audit behavior. All collectors register into the shared registry.
type ExecutiveMetrics struct {
	govOps      *prometheus.CounterVec // labels: operation, outcome
	auditAppend prometheus.Histogram
	startupDur  prometheus.Gauge
	shutdownDur prometheus.Gauge
	depUp       *prometheus.GaugeVec // label: dependency
	startupFail prometheus.Counter
	shutdownTO  prometheus.Counter
	invBreached prometheus.Gauge
	invCheckErr prometheus.Counter
}

// NewExecutiveMetrics builds and registers the executive collectors.
func NewExecutiveMetrics(reg prometheus.Registerer) *ExecutiveMetrics {
	m := &ExecutiveMetrics{
		govOps: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "oneops_governance_operations_total",
			Help: "Governance operations that reached the atomic audit emit, by operation and outcome.",
		}, []string{"operation", "outcome"}),
		auditAppend: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "oneops_audit_append_duration_seconds",
			Help:    "Latency of the in-transaction audit append (ADR-AUDIT-005).",
			Buckets: prometheus.DefBuckets,
		}),
		startupDur: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "oneops_startup_duration_seconds",
			Help: "Wall-clock duration of the last successful startup sequence.",
		}),
		shutdownDur: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "oneops_shutdown_duration_seconds",
			Help: "Wall-clock duration of the last graceful shutdown.",
		}),
		depUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "oneops_dependency_up",
			Help: "1 if the named dependency is healthy, else 0.",
		}, []string{"dependency"}),
		startupFail: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_startup_failures_total",
			Help: "Total startup failures observed by the supervisor.",
		}),
		shutdownTO: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_shutdown_timeouts_total",
			Help: "Total graceful-shutdown timeouts.",
		}),
		invBreached: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "oneops_invariant_breached",
			Help: "1 if a continuously-verified platform invariant is breached and the instance is refusing to serve tenant data (ADR-SECURITY-002), else 0.",
		}),
		invCheckErr: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_invariant_check_failures_total",
			Help: "Total times the invariant re-verification could not run (the previous verdict was carried).",
		}),
	}
	reg.MustRegister(m.govOps, m.auditAppend, m.startupDur, m.shutdownDur, m.depUp, m.startupFail, m.shutdownTO,
		m.invBreached, m.invCheckErr)
	return m
}

// ObserveAuditAppend records one in-transaction audit append and the governance
// operation it completes (an AppendTx call is a 1:1 signal of a governance
// operation reaching the atomic emit point).
func (m *ExecutiveMetrics) ObserveAuditAppend(operation string, dur time.Duration, ok bool) {
	m.auditAppend.Observe(dur.Seconds())
	m.govOps.WithLabelValues(operation, outcomeLabel(ok)).Inc()
}

// SetStartupDuration records the last startup duration.
func (m *ExecutiveMetrics) SetStartupDuration(d time.Duration) { m.startupDur.Set(d.Seconds()) }

// SetShutdownDuration records the last shutdown duration.
func (m *ExecutiveMetrics) SetShutdownDuration(d time.Duration) { m.shutdownDur.Set(d.Seconds()) }

// SetDependencyUp records a dependency's health as an alerting signal.
func (m *ExecutiveMetrics) SetDependencyUp(name string, up bool) {
	v := 0.0
	if up {
		v = 1
	}
	m.depUp.WithLabelValues(name).Set(v)
}

// IncStartupFailure counts a startup failure.
func (m *ExecutiveMetrics) IncStartupFailure() { m.startupFail.Inc() }

// IncShutdownTimeout counts a graceful-shutdown timeout.
func (m *ExecutiveMetrics) IncShutdownTimeout() { m.shutdownTO.Inc() }

func outcomeLabel(ok bool) string {
	if ok {
		return "success"
	}
	return "failure"
}

// MeteredAuditor decorates a governance.Auditor to record audit-append latency and
// governance-operation counts without modifying the governance engine or the audit
// appender. It changes no behavior: it calls the inner appender and returns its
// result unchanged. Wire it in place of the raw appender at composition time.
type MeteredAuditor struct {
	inner governance.Auditor
	m     *ExecutiveMetrics
}

var _ governance.Auditor = (*MeteredAuditor)(nil)

// NewMeteredAuditor wraps inner. If inner or m is nil it returns inner unwrapped
// (best-effort: instrumentation is never allowed to break the governed path).
func NewMeteredAuditor(inner governance.Auditor, m *ExecutiveMetrics) governance.Auditor {
	if inner == nil || m == nil {
		return inner
	}
	return &MeteredAuditor{inner: inner, m: m}
}

// AppendTx times the inner append, records metrics, and returns its result
// unchanged (errors propagated verbatim).
func (a *MeteredAuditor) AppendTx(ctx context.Context, tx pgx.Tx, in audit.AppendInput) (domain.AuditEvent, error) {
	start := time.Now()
	ev, err := a.inner.AppendTx(ctx, tx, in)
	a.m.ObserveAuditAppend(string(in.Operation), time.Since(start), err == nil)
	return ev, err
}

// SetInvariantBreached records whether a continuously-verified invariant is
// currently breached (ADR-SECURITY-002). This is the signal to alert on: while
// it is 1 the instance is refusing tenant traffic and its workers are stopped.
func (m *ExecutiveMetrics) SetInvariantBreached(breached bool) {
	if breached {
		m.invBreached.Set(1)
		return
	}
	m.invBreached.Set(0)
}

// IncInvariantCheckFailure counts a re-verification that could not run, which
// leaves the previous verdict standing rather than failing the platform closed
// on a transient database blip.
func (m *ExecutiveMetrics) IncInvariantCheckFailure() { m.invCheckErr.Inc() }
