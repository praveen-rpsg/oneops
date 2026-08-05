package grouping

import "github.com/prometheus/client_golang/prometheus"

// PromMetrics is the Prometheus Metrics implementation, mirroring
// alerting.PromMetrics. No tenant_id/incident_id label, the same cardinality
// restraint every other background worker's metrics in this platform apply.
type PromMetrics struct {
	tenantsProcessed  prometheus.Counter
	incidentsChanged  prometheus.Counter
	errors            prometheus.Counter
}

var _ Metrics = (*PromMetrics)(nil)

// NewPromMetrics builds and registers the reconciler's collectors.
func NewPromMetrics(reg prometheus.Registerer) *PromMetrics {
	m := &PromMetrics{
		tenantsProcessed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_incident_grouping_tenants_processed_total",
			Help: "Total tenant reconciliation passes performed (E4.2).",
		}),
		incidentsChanged: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_incident_grouping_incidents_changed_total",
			Help: "Total incidents whose root_incident_id was written (set, cleared, or re-pointed) by the reconciler (E4.2).",
		}),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_incident_grouping_errors_total",
			Help: "Total reconciliation errors (list, walk, or write failures).",
		}),
	}
	reg.MustRegister(m.tenantsProcessed, m.incidentsChanged, m.errors)
	return m
}

// IncTenantsProcessed implements Metrics.
func (m *PromMetrics) IncTenantsProcessed() { m.tenantsProcessed.Inc() }

// IncIncidentsChanged implements Metrics.
func (m *PromMetrics) IncIncidentsChanged() { m.incidentsChanged.Inc() }

// IncErrors implements Metrics.
func (m *PromMetrics) IncErrors() { m.errors.Inc() }
