package httpapi

import (
	"net/http"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// incidentTrendSeverityCountsDTO is one bucket's opened-incident breakdown by
// severity (E9.1).
type incidentTrendSeverityCountsDTO struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
}

// incidentTrendSourceCountsDTO is one bucket's opened-incident breakdown by
// source — all four of domain.IncidentSource's closed vocabulary
// (manual/alert/security/vuln, E9.3) are broken out here so OpenedTotal
// always equals the sum of this struct's fields. alert remains the honest
// alert-volume proxy (ADR-NOC-008): it is domain.Incident.Source (E4.1), NOT
// a true alert-firing log. There is no alert_event table in this system;
// alert counts incidents E4.1's correlation pipeline created or linked off a
// firing, never a raw firing count. security/vuln are E8/E8.3's SOC and
// vuln-remediation incidents (domain.NewSecurityIncident /
// domain.NewVulnRemediationIncident) — previously counted in OpenedTotal but
// silently dropped from this breakdown (E9.3 closes that gap).
type incidentTrendSourceCountsDTO struct {
	Manual   int `json:"manual"`
	Alert    int `json:"alert"`
	Security int `json:"security"`
	Vuln     int `json:"vuln"`
}

// incidentTrendPointDTO is one contiguous, boundary-aligned bucket of GET
// /admin/dashboards/incident-trends' response (E9.1). A bucket with no
// matching incidents still appears here, zeroed — see
// buildIncidentTrendsResponse's own doc comment.
type incidentTrendPointDTO struct {
	BucketStart      time.Time                      `json:"bucket_start"`
	OpenedTotal      int                            `json:"opened_total"`
	OpenedBySeverity incidentTrendSeverityCountsDTO `json:"opened_by_severity"`
	OpenedBySource   incidentTrendSourceCountsDTO   `json:"opened_by_source"`
	ResolvedTotal    int                            `json:"resolved_total"`
}

// incidentTrendsResponseDTO is GET /admin/dashboards/incident-trends'
// transient response (E9.1, ADR-NOC-008): a computed-at-request-time
// projection over incident, never a stored Dashboard/Report entity
// (docs/PLATFORM-BUILD-PLAN.md §4). from/to/bucket echo the validated,
// normalised (UTC) request back, so a client can align its own axis labels
// without re-deriving the bucketing rules.
type incidentTrendsResponseDTO struct {
	From   time.Time               `json:"from"`
	To     time.Time               `json:"to"`
	Bucket string                  `json:"bucket"`
	Points []incidentTrendPointDTO `json:"points"`
}

// buildIncidentTrendsResponse assembles the contiguous, zero-filled series a
// client sees from the store's raw, ungrouped-by-bucket rows. The bucket
// "shape" (how many, and their bucket_start values) comes ENTIRELY from
// q.BucketStarts — never from which buckets happen to appear in opened/
// resolved — so a window with zero matching incidents still returns every
// slot, zeroed, and a row the store returns for a bucket outside that shape
// (which q.Validate having already run should make impossible) is silently
// dropped rather than panicking on a missing map key.
func buildIncidentTrendsResponse(
	q domain.IncidentTrendsQuery, opened []domain.IncidentOpenedTrendPoint, resolved []domain.IncidentResolvedTrendPoint,
) incidentTrendsResponseDTO {
	starts := q.BucketStarts()
	points := make([]incidentTrendPointDTO, len(starts))
	index := make(map[time.Time]int, len(starts))
	for i, bs := range starts {
		points[i] = incidentTrendPointDTO{BucketStart: bs}
		index[bs] = i
	}

	for _, o := range opened {
		i, ok := index[o.BucketStart.UTC()]
		if !ok {
			continue
		}
		p := &points[i]
		p.OpenedTotal += o.Count
		switch o.Severity {
		case domain.IncidentSeverityCritical:
			p.OpenedBySeverity.Critical += o.Count
		case domain.IncidentSeverityHigh:
			p.OpenedBySeverity.High += o.Count
		case domain.IncidentSeverityMedium:
			p.OpenedBySeverity.Medium += o.Count
		case domain.IncidentSeverityLow:
			p.OpenedBySeverity.Low += o.Count
		}
		switch o.Source {
		case domain.IncidentSourceManual:
			p.OpenedBySource.Manual += o.Count
		case domain.IncidentSourceAlert:
			p.OpenedBySource.Alert += o.Count
		case domain.IncidentSourceSecurity:
			p.OpenedBySource.Security += o.Count
		case domain.IncidentSourceVuln:
			p.OpenedBySource.Vuln += o.Count
		default:
			// An unrecognized source stays in OpenedTotal (already added
			// above) but intentionally does not land in any bucket here —
			// no invented "other" catch-all ahead of a real future source.
		}
	}
	for _, r := range resolved {
		i, ok := index[r.BucketStart.UTC()]
		if !ok {
			continue
		}
		points[i].ResolvedTotal += r.Count
	}

	return incidentTrendsResponseDTO{
		From:   q.From.UTC(),
		To:     q.To.UTC(),
		Bucket: string(q.Bucket),
		Points: points,
	}
}

// incidentTrends serves GET /admin/dashboards/incident-trends (E9.1,
// ADR-NOC-008): incident volume over time, bucketed at hour/day granularity,
// split by severity and by source — the data source E9.2's dashboards screen
// needs. Tenant isolation is row-level security alone, exactly like GET
// /admin/noc/overview (ADR-NOC-001): incident is TENANT-OWNED and this
// handler's store is the same appPool-scoped domain.IncidentRepository
// instance every other incident route already uses — no explicit tenant
// predicate, no privileged pool. from/to/bucket are validated and BOUNDED
// (domain.IncidentTrendsQuery.Validate, at most MaxIncidentTrendBuckets
// contiguous buckets) before any query runs.
func (s *Server) incidentTrends(w http.ResponseWriter, r *http.Request) {
	if s.incidents == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "incident administration is not configured")
		return
	}

	q := r.URL.Query()
	fromRaw, toRaw, bucketRaw := q.Get("from"), q.Get("to"), q.Get("bucket")
	if fromRaw == "" {
		writeProblem(w, r, http.StatusUnprocessableEntity, "invalid from", "from is required")
		return
	}
	if toRaw == "" {
		writeProblem(w, r, http.StatusUnprocessableEntity, "invalid to", "to is required")
		return
	}
	if bucketRaw == "" {
		writeProblem(w, r, http.StatusUnprocessableEntity, "invalid bucket", "bucket is required")
		return
	}

	from, err := time.Parse(time.RFC3339, fromRaw)
	if err != nil {
		writeProblem(w, r, http.StatusUnprocessableEntity, "invalid from", "from must be an RFC3339 timestamp")
		return
	}
	to, err := time.Parse(time.RFC3339, toRaw)
	if err != nil {
		writeProblem(w, r, http.StatusUnprocessableEntity, "invalid to", "to must be an RFC3339 timestamp")
		return
	}

	query := domain.IncidentTrendsQuery{From: from, To: to, Bucket: domain.IncidentTrendBucket(bucketRaw)}
	// Validate at the transport edge, not only in the store — mirrors
	// listAdminAudit's own discipline: a caller must be refused by the
	// surface it called, not by whichever implementation happens to be
	// wired behind it.
	if err := query.Validate(); err != nil {
		s.mapError(w, r, err)
		return
	}

	opened, resolved, err := s.incidents.Trends(r.Context(), query)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, buildIncidentTrendsResponse(query, opened, resolved))
}
