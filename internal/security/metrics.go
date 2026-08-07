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

// ResponderPromMetrics is the Prometheus ResponderMetrics implementation
// (E8.5) — a SEPARATE type/metric-name family from PromMetrics/
// IOCMatcherPromMetrics above, the same split each of those draws: "security
// incidents checked against response rules" and "SAFE actions dispatched"
// are their own signals a dashboard needs to tell apart from either
// detection pipeline's own counters.
type ResponderPromMetrics struct {
	incidentsChecked         prometheus.Counter
	matched                  prometheus.Counter
	dispatched               prometheus.Counter
	skippedAlreadyDispatched prometheus.Counter
	tenantsProcessed         prometheus.Counter
	errors                   prometheus.Counter
}

var _ ResponderMetrics = (*ResponderPromMetrics)(nil)

// NewResponderPromMetrics builds and registers the responder's collectors.
func NewResponderPromMetrics(reg prometheus.Registerer) *ResponderPromMetrics {
	m := &ResponderPromMetrics{
		incidentsChecked: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_security_response_incidents_checked_total", Help: "Total security-sourced incidents checked against response rules.",
		}),
		matched: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_security_response_matches_total", Help: "Total (incident, rule) pairs that satisfied a response rule's condition.",
		}),
		dispatched: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_security_response_dispatched_total", Help: "Total SAFE actions successfully run.",
		}),
		skippedAlreadyDispatched: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_security_response_skipped_already_dispatched_total", Help: "Total matches skipped because the (incident, rule) pair was already claimed.",
		}),
		tenantsProcessed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_security_response_tenants_processed_total", Help: "Total per-tenant response passes completed.",
		}),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_security_response_errors_total", Help: "Total responder errors (list, read, claim, or action failures).",
		}),
	}
	reg.MustRegister(m.incidentsChecked, m.matched, m.dispatched, m.skippedAlreadyDispatched, m.tenantsProcessed, m.errors)
	return m
}

// IncIncidentsChecked implements ResponderMetrics.
func (m *ResponderPromMetrics) IncIncidentsChecked() { m.incidentsChecked.Inc() }

// IncMatched implements ResponderMetrics.
func (m *ResponderPromMetrics) IncMatched() { m.matched.Inc() }

// IncDispatched implements ResponderMetrics.
func (m *ResponderPromMetrics) IncDispatched() { m.dispatched.Inc() }

// IncSkippedAlreadyDispatched implements ResponderMetrics.
func (m *ResponderPromMetrics) IncSkippedAlreadyDispatched() { m.skippedAlreadyDispatched.Inc() }

// IncTenantsProcessed implements ResponderMetrics.
func (m *ResponderPromMetrics) IncTenantsProcessed() { m.tenantsProcessed.Inc() }

// IncErrors implements ResponderMetrics.
func (m *ResponderPromMetrics) IncErrors() { m.errors.Inc() }
