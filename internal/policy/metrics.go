package policy

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics receives policy-automation observability signals.
type Metrics interface {
	IncExecution()
	IncFailure()
	IncRetry()
	ObserveDuration(d time.Duration)
	SetActivePolicies(n int)
}

// NopMetrics discards all signals.
type NopMetrics struct{}

// IncExecution implements Metrics.
func (NopMetrics) IncExecution() {}

// IncFailure implements Metrics.
func (NopMetrics) IncFailure() {}

// IncRetry implements Metrics.
func (NopMetrics) IncRetry() {}

// ObserveDuration implements Metrics.
func (NopMetrics) ObserveDuration(time.Duration) {}

// SetActivePolicies implements Metrics.
func (NopMetrics) SetActivePolicies(int) {}

// PromMetrics is the Prometheus Metrics implementation.
type PromMetrics struct {
	executions prometheus.Counter
	failures   prometheus.Counter
	retries    prometheus.Counter
	duration   prometheus.Histogram
	active     prometheus.Gauge
}

var _ Metrics = (*PromMetrics)(nil)

// NewPromMetrics builds and registers the policy collectors into the shared
// registry (no duplication of existing metrics).
func NewPromMetrics(reg prometheus.Registerer) *PromMetrics {
	m := &PromMetrics{
		executions: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_policy_executions_total", Help: "Total policy executions attempted.",
		}),
		failures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_policy_failures_total", Help: "Total failed policy action attempts.",
		}),
		retries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_policy_retries_total", Help: "Total policy execution retries scheduled.",
		}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "oneops_policy_execution_duration_seconds", Help: "Policy action execution duration.",
			Buckets: prometheus.DefBuckets,
		}),
		active: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "oneops_policy_active", Help: "Currently enabled policies.",
		}),
	}
	reg.MustRegister(m.executions, m.failures, m.retries, m.duration, m.active)
	return m
}

// IncExecution implements Metrics.
func (m *PromMetrics) IncExecution() { m.executions.Inc() }

// IncFailure implements Metrics.
func (m *PromMetrics) IncFailure() { m.failures.Inc() }

// IncRetry implements Metrics.
func (m *PromMetrics) IncRetry() { m.retries.Inc() }

// ObserveDuration implements Metrics.
func (m *PromMetrics) ObserveDuration(d time.Duration) { m.duration.Observe(d.Seconds()) }

// SetActivePolicies implements Metrics.
func (m *PromMetrics) SetActivePolicies(n int) { m.active.Set(float64(n)) }
