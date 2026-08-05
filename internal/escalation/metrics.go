package escalation

import "github.com/prometheus/client_golang/prometheus"

// PromMetrics is the Prometheus Metrics implementation, mirroring
// notification.PromMetrics/grouping.PromMetrics. No tenant_id/incident_id
// label, the same cardinality restraint every other background worker's
// metrics in this platform apply.
type PromMetrics struct {
	seeded          prometheus.Counter
	skippedNoPolicy prometheus.Counter
	paged           prometheus.Counter
	escalated       prometheus.Counter
	skippedRevoked  prometheus.Counter
	acked           prometheus.Counter
	resolved        prometheus.Counter
	exhausted       prometheus.Counter
	errors          prometheus.Counter
}

var _ Metrics = (*PromMetrics)(nil)

// NewPromMetrics builds and registers the escalation engine's collectors.
func NewPromMetrics(reg prometheus.Registerer) *PromMetrics {
	m := &PromMetrics{
		seeded: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_escalation_seeded_total", Help: "Total escalation_state rows seeded for newly-open, unacknowledged, alert-sourced incidents (E5.2b-2).",
		}),
		skippedNoPolicy: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_escalation_seed_skipped_no_policy_total", Help: "Total eligible incidents left unseeded because their tenant has no active escalation policy.",
		}),
		paged: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_escalation_paged_total", Help: "Total escalation tiers actually paged (a Notification enqueued).",
		}),
		escalated: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_escalation_escalated_total", Help: "Total tier-index advances, whether or not the tier was actually paged.",
		}),
		skippedRevoked: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_escalation_skipped_revoked_total", Help: "Total tiers NOT paged because there was no on-call user or the resolved user is not an active tenant member.",
		}),
		acked: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_escalation_acked_total", Help: "Total escalation_state rows stopped because the incident was acknowledged.",
		}),
		resolved: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_escalation_resolved_total", Help: "Total escalation_state rows stopped because the incident left the open/acknowledged pair some other way.",
		}),
		exhausted: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_escalation_exhausted_total", Help: "Total escalation_state rows that reached the end of their policy's ladder still unacknowledged.",
		}),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "oneops_escalation_errors_total", Help: "Total escalation engine errors (seed, claim, read, or write failures).",
		}),
	}
	reg.MustRegister(m.seeded, m.skippedNoPolicy, m.paged, m.escalated, m.skippedRevoked,
		m.acked, m.resolved, m.exhausted, m.errors)
	return m
}

func (m *PromMetrics) IncSeeded(n int)          { m.seeded.Add(float64(n)) }
func (m *PromMetrics) IncSkippedNoPolicy(n int) { m.skippedNoPolicy.Add(float64(n)) }
func (m *PromMetrics) IncPaged()                { m.paged.Inc() }
func (m *PromMetrics) IncEscalated()            { m.escalated.Inc() }
func (m *PromMetrics) IncSkippedRevoked()       { m.skippedRevoked.Inc() }
func (m *PromMetrics) IncAcked()                { m.acked.Inc() }
func (m *PromMetrics) IncResolved()             { m.resolved.Inc() }
func (m *PromMetrics) IncExhausted()            { m.exhausted.Inc() }
func (m *PromMetrics) IncErrors()               { m.errors.Inc() }
