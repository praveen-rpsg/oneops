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

// IOCMatcherPromMetrics is the Prometheus IOCMatcherMetrics implementation
// (E8.2b) — a SEPARATE type/metric-name family from PromMetrics above, the
// same split IOCMatcherMetrics's own doc comment draws: "rules evaluated" and
// "observations checked against the watchlist" are different signals.
type IOCMatcherPromMetrics struct {
	observationsChecked prometheus.Counter
	matched             prometheus.Counter
	tenantsProcessed    prometheus.Counter
	errors              prometheus.Counter
}

var _ IOCMatcherMetrics = (*IOCMatcherPromMetrics)(nil)

// NewIOCMatcherPromMetrics builds and registers the matcher's collectors.
func NewIOCMatcherPromMetrics(reg prometheus.Registerer) *IOCMatcherPromMetrics {
	m := &IOCMatcherPromMetrics{
		observationsChecked: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_ioc_observations_checked_total", Help: "Total security_observation rows checked against the IOC watchlist.",
		}),
		matched: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_ioc_matches_total", Help: "Total observations that matched an enabled ioc.",
		}),
		tenantsProcessed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_ioc_matcher_tenants_processed_total", Help: "Total per-tenant match passes completed.",
		}),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_ioc_matcher_errors_total", Help: "Total IOC matcher errors (list, read, or correlate failures).",
		}),
	}
	reg.MustRegister(m.observationsChecked, m.matched, m.tenantsProcessed, m.errors)
	return m
}

// IncObservationsChecked implements IOCMatcherMetrics.
func (m *IOCMatcherPromMetrics) IncObservationsChecked() { m.observationsChecked.Inc() }

// IncMatched implements IOCMatcherMetrics.
func (m *IOCMatcherPromMetrics) IncMatched() { m.matched.Inc() }

// IncTenantsProcessed implements IOCMatcherMetrics.
func (m *IOCMatcherPromMetrics) IncTenantsProcessed() { m.tenantsProcessed.Inc() }

// IncErrors implements IOCMatcherMetrics.
func (m *IOCMatcherPromMetrics) IncErrors() { m.errors.Inc() }
