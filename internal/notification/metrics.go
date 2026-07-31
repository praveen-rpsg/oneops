package notification

import "github.com/prometheus/client_golang/prometheus"

// PromMetrics is the Prometheus Metrics implementation. It registers only the
// notification-delivery collectors into the shared registry (no duplication),
// mirroring events.PromMetrics. No channel or tenant label: cardinality must
// stay bounded regardless of how many tenants or channels exist
// (ADR-SECURITY-004's reasoning, applied here).
type PromMetrics struct {
	delivered prometheus.Counter
	failed    prometheus.Counter
}

var _ Metrics = (*PromMetrics)(nil)

// NewPromMetrics builds and registers the notification-delivery collectors.
func NewPromMetrics(reg prometheus.Registerer) *PromMetrics {
	m := &PromMetrics{
		delivered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_notification_delivered_total", Help: "Total notifications successfully delivered.",
		}),
		failed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_notification_failed_total", Help: "Total notification delivery attempts that failed.",
		}),
	}
	reg.MustRegister(m.delivered, m.failed)
	return m
}

// IncDelivered implements Metrics.
func (m *PromMetrics) IncDelivered() { m.delivered.Inc() }

// IncFailed implements Metrics.
func (m *PromMetrics) IncFailed() { m.failed.Inc() }
