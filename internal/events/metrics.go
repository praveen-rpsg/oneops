package events

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics receives delivery observability signals. NopMetrics discards them.
type Metrics interface {
	IncDelivered()
	IncFailure()
	IncRetry()
	ObserveDeliveryLatency(d time.Duration)
	SetActiveWebhooks(n int)
}

// NopMetrics is the no-op Metrics.
type NopMetrics struct{}

// IncDelivered implements Metrics.
func (NopMetrics) IncDelivered() {}

// IncFailure implements Metrics.
func (NopMetrics) IncFailure() {}

// IncRetry implements Metrics.
func (NopMetrics) IncRetry() {}

// ObserveDeliveryLatency implements Metrics.
func (NopMetrics) ObserveDeliveryLatency(time.Duration) {}

// SetActiveWebhooks implements Metrics.
func (NopMetrics) SetActiveWebhooks(int) {}

// PromMetrics is the Prometheus Metrics implementation. It registers only the
// event-delivery collectors into the shared registry (no duplication).
type PromMetrics struct {
	delivered prometheus.Counter
	failures  prometheus.Counter
	retries   prometheus.Counter
	latency   prometheus.Histogram
	active    prometheus.Gauge
}

var _ Metrics = (*PromMetrics)(nil)

// NewPromMetrics builds and registers the event-delivery collectors.
func NewPromMetrics(reg prometheus.Registerer) *PromMetrics {
	m := &PromMetrics{
		delivered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_webhook_deliveries_total", Help: "Total successful webhook deliveries.",
		}),
		failures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_webhook_failures_total", Help: "Total failed webhook delivery attempts.",
		}),
		retries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_webhook_retries_total", Help: "Total webhook delivery retries scheduled.",
		}),
		latency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "oneops_webhook_delivery_latency_seconds", Help: "Webhook delivery attempt latency.",
			Buckets: prometheus.DefBuckets,
		}),
		active: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "oneops_webhook_active", Help: "Currently enabled webhooks.",
		}),
	}
	reg.MustRegister(m.delivered, m.failures, m.retries, m.latency, m.active)
	return m
}

// IncDelivered implements Metrics.
func (m *PromMetrics) IncDelivered() { m.delivered.Inc() }

// IncFailure implements Metrics.
func (m *PromMetrics) IncFailure() { m.failures.Inc() }

// IncRetry implements Metrics.
func (m *PromMetrics) IncRetry() { m.retries.Inc() }

// ObserveDeliveryLatency implements Metrics.
func (m *PromMetrics) ObserveDeliveryLatency(d time.Duration) { m.latency.Observe(d.Seconds()) }

// SetActiveWebhooks implements Metrics.
func (m *PromMetrics) SetActiveWebhooks(n int) { m.active.Set(float64(n)) }
