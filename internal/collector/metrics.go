package collector

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/rpsg/oneops/internal/domain"
)

// PromMetrics is the Prometheus Metrics implementation, mirroring
// notification.PromMetrics. The status label has exactly three possible
// values (domain.CollectorCheckStatus.Valid()), so cardinality stays bounded
// regardless of how many checks or tenants exist — no check_id or tenant_id
// label, the same restraint notification.PromMetrics documents.
type PromMetrics struct {
	checked *prometheus.CounterVec
}

var _ Metrics = (*PromMetrics)(nil)

// NewPromMetrics builds and registers the scheduler's collectors.
func NewPromMetrics(reg prometheus.Registerer) *PromMetrics {
	m := &PromMetrics{
		checked: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "oneops_collector_checks_total", Help: "Total collector checks executed, by outcome.",
		}, []string{"status"}),
	}
	reg.MustRegister(m.checked)
	return m
}

// IncChecked implements Metrics.
func (m *PromMetrics) IncChecked(status domain.CollectorCheckStatus) {
	m.checked.WithLabelValues(string(status)).Inc()
}
