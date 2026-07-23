package ops

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/rpsg/oneops/internal/domain"
)

// PromMetrics is the Prometheus Recorder for audit-integrity verification. It
// registers a small, bounded set of aggregate collectors (no per-chain labels,
// to avoid cardinality blow-up) into the caller's registry. It satisfies Recorder.
type PromMetrics struct {
	runs        prometheus.Counter
	chains      prometheus.Counter
	failures    prometheus.Counter
	errors      prometheus.Counter
	duration    prometheus.Histogram
	lastRunTime prometheus.Gauge
	integrityOK prometheus.Gauge
}

var _ Recorder = (*PromMetrics)(nil)

// NewPromMetrics builds and registers the verification collectors. Reusing the
// application's shared registry keeps a single /metrics exposition.
func NewPromMetrics(reg prometheus.Registerer) *PromMetrics {
	m := &PromMetrics{
		runs: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_audit_verification_runs_total",
			Help: "Total audit-integrity verification sweeps performed.",
		}),
		chains: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_audit_chains_verified_total",
			Help: "Total audit chains verified across all sweeps.",
		}),
		failures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_audit_verification_failures_total",
			Help: "Total detected audit-chain integrity breaks.",
		}),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_audit_verification_errors_total",
			Help: "Total audit-chain verification transport/timeout errors.",
		}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "oneops_audit_verification_duration_seconds",
			Help:    "Per-chain audit verification duration in seconds.",
			Buckets: prometheus.DefBuckets,
		}),
		lastRunTime: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "oneops_audit_last_verification_timestamp_seconds",
			Help: "Unix time of the last completed verification sweep.",
		}),
		integrityOK: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "oneops_audit_integrity_ok",
			Help: "1 if the last sweep found no breaks or errors, else 0.",
		}),
	}
	reg.MustRegister(m.runs, m.chains, m.failures, m.errors, m.duration, m.lastRunTime, m.integrityOK)
	return m
}

// ChainVerified implements Recorder.
func (m *PromMetrics) ChainVerified(_ string, res domain.VerifyResult, dur time.Duration) {
	m.chains.Inc()
	m.duration.Observe(dur.Seconds())
	if !res.OK {
		m.failures.Inc()
	}
}

// ChainError implements Recorder.
func (m *PromMetrics) ChainError(_ string, dur time.Duration) {
	m.errors.Inc()
	m.duration.Observe(dur.Seconds())
}

// RunCompleted implements Recorder.
func (m *PromMetrics) RunCompleted(report IntegrityReport) {
	m.runs.Inc()
	m.lastRunTime.SetToCurrentTime()
	if report.Healthy() {
		m.integrityOK.Set(1)
	} else {
		m.integrityOK.Set(0)
	}
}
