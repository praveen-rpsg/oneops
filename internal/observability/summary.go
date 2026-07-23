package observability

// summaryMetrics is the whitelist of operational counters/gauges surfaced by the
// admin metrics-summary endpoint. It derives from the SAME registry that backs
// /metrics — it does not define or duplicate any collector.
var summaryMetrics = map[string]bool{
	"http_requests_total":                      true,
	"oneops_governance_operations_total":       true,
	"oneops_audit_verification_runs_total":     true,
	"oneops_audit_chains_verified_total":       true,
	"oneops_audit_verification_failures_total": true,
	"oneops_audit_verification_errors_total":   true,
	"oneops_audit_integrity_ok":                true,
	"oneops_startup_failures_total":            true,
	"oneops_shutdown_timeouts_total":           true,
}

// Summary gathers the shared Prometheus registry and returns a compact map of
// whitelisted operational counters (summed across label sets; histograms reduce
// to their sample count). It reuses the existing metrics — nothing is recomputed
// or re-registered.
func (m *Metrics) Summary() (map[string]float64, error) {
	families, err := m.reg.Gather()
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(summaryMetrics))
	for _, mf := range families {
		if !summaryMetrics[mf.GetName()] {
			continue
		}
		var sum float64
		for _, metric := range mf.GetMetric() {
			switch {
			case metric.GetCounter() != nil:
				sum += metric.GetCounter().GetValue()
			case metric.GetGauge() != nil:
				sum += metric.GetGauge().GetValue()
			case metric.GetHistogram() != nil:
				sum += float64(metric.GetHistogram().GetSampleCount())
			}
		}
		out[mf.GetName()] = sum
	}
	return out, nil
}
