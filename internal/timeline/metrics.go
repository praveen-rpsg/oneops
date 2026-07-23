package timeline

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// PromMetrics is the Prometheus Metrics implementation for timeline queries.
type PromMetrics struct {
	queries  prometheus.Counter
	duration prometheus.Histogram
}

var _ Metrics = (*PromMetrics)(nil)

// NewPromMetrics builds and registers the two timeline collectors into the shared
// registry (no duplication of existing metrics).
func NewPromMetrics(reg prometheus.Registerer) *PromMetrics {
	m := &PromMetrics{
		queries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_timeline_queries_total", Help: "Total execution-timeline queries served.",
		}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "oneops_timeline_query_duration_seconds", Help: "Execution-timeline query duration.",
			Buckets: prometheus.DefBuckets,
		}),
	}
	reg.MustRegister(m.queries, m.duration)
	return m
}

// IncQuery implements Metrics.
func (m *PromMetrics) IncQuery() { m.queries.Inc() }

// ObserveDuration implements Metrics.
func (m *PromMetrics) ObserveDuration(d time.Duration) { m.duration.Observe(d.Seconds()) }
