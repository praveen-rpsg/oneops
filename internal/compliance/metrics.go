package compliance

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// PromMetrics is the Prometheus Metrics implementation for compliance queries.
type PromMetrics struct {
	queries  prometheus.Counter
	exports  prometheus.Counter
	duration prometheus.Histogram
}

var _ Metrics = (*PromMetrics)(nil)

// NewPromMetrics builds and registers the three compliance collectors into the
// shared registry (no duplication of existing metrics).
func NewPromMetrics(reg prometheus.Registerer) *PromMetrics {
	m := &PromMetrics{
		queries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_compliance_queries_total", Help: "Total compliance queries served.",
		}),
		exports: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_evidence_exports_total", Help: "Total evidence bundle exports.",
		}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "oneops_compliance_query_duration_seconds", Help: "Compliance query duration.",
			Buckets: prometheus.DefBuckets,
		}),
	}
	reg.MustRegister(m.queries, m.exports, m.duration)
	return m
}

// IncQuery implements Metrics.
func (m *PromMetrics) IncQuery() { m.queries.Inc() }

// IncExport implements Metrics.
func (m *PromMetrics) IncExport() { m.exports.Inc() }

// ObserveDuration implements Metrics.
func (m *PromMetrics) ObserveDuration(d time.Duration) { m.duration.Observe(d.Seconds()) }
