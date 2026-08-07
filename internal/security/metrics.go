package security

import "github.com/prometheus/client_golang/prometheus"

// PromMetrics is the Prometheus Metrics implementation, mirroring
// alerting.PromMetrics. No rule_id or tenant_id label, the same cardinality
// restraint alerting.PromMetrics documents.
type PromMetrics struct {
	evaluated prometheus.Counter
	fired     prometheus.Counter
	recovered prometheus.Counter
	errors    prometheus.Counter
}

var _ Metrics = (*PromMetrics)(nil)

// NewPromMetrics builds and registers the detector's collectors.
func NewPromMetrics(reg prometheus.Registerer) *PromMetrics {
	m := &PromMetrics{
		evaluated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_security_rules_evaluated_total", Help: "Total security rule evaluations performed.",
		}),
		fired: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_security_rules_fired_total", Help: "Total ok->firing transitions.",
		}),
		recovered: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_security_rules_recovered_total", Help: "Total firing->ok transitions.",
		}),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_security_rules_evaluation_errors_total", Help: "Total evaluation errors (count read, correlate, or persist failures).",
		}),
	}
	reg.MustRegister(m.evaluated, m.fired, m.recovered, m.errors)
	return m
}

// IncEvaluated implements Metrics.
func (m *PromMetrics) IncEvaluated() { m.evaluated.Inc() }

// IncFired implements Metrics.
func (m *PromMetrics) IncFired() { m.fired.Inc() }

// IncRecovered implements Metrics.
func (m *PromMetrics) IncRecovered() { m.recovered.Inc() }

// IncErrors implements Metrics.
func (m *PromMetrics) IncErrors() { m.errors.Inc() }
