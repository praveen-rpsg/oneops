package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func adminIncidentTrendsToken(t *testing.T) string {
	t.Helper()
	return mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"})
}

func wireIncidentTrends(srv *Server, incidents *fakeIncidents) {
	srv.SetIncidents(incidents)
}

// ---------------------------------------------------------- authorization

func TestIncidentTrends_Authorization(t *testing.T) {
	cases := []struct {
		name  string
		token func(t *testing.T) string
		want  int
	}{
		{"no token", func(*testing.T) string { return "" }, http.StatusUnauthorized},
		{"read-only principal", func(t *testing.T) string {
			return mintRoleTenantToken(t, "ext-acme", []string{"oneops-reader"})
		}, http.StatusForbidden},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer(true)
			srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
			incidents := &fakeIncidents{}
			wireIncidentTrends(srv, incidents)

			req := httptest.NewRequest(http.MethodGet,
				"/v1/admin/dashboards/incident-trends?from=2026-08-01T00:00:00Z&to=2026-08-02T00:00:00Z&bucket=hour", nil)
			if tok := tc.token(t); tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if incidents.trendsCalls != 0 {
				t.Error("a refused request reached the incident store")
			}
		})
	}
}

func TestIncidentTrends_NotImplementedWhenUnwired(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))

	req := httptest.NewRequest(http.MethodGet,
		"/v1/admin/dashboards/incident-trends?from=2026-08-01T00:00:00Z&to=2026-08-02T00:00:00Z&bucket=hour", nil)
	req.Header.Set("Authorization", "Bearer "+adminIncidentTrendsToken(t))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------- param validation

func TestIncidentTrends_ParamValidation(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"missing from", "to=2026-08-02T00:00:00Z&bucket=hour"},
		{"missing to", "from=2026-08-01T00:00:00Z&bucket=hour"},
		{"missing bucket", "from=2026-08-01T00:00:00Z&to=2026-08-02T00:00:00Z"},
		{"bad from", "from=not-a-time&to=2026-08-02T00:00:00Z&bucket=hour"},
		{"bad to", "from=2026-08-01T00:00:00Z&to=not-a-time&bucket=hour"},
		{"bad bucket", "from=2026-08-01T00:00:00Z&to=2026-08-02T00:00:00Z&bucket=week"},
		{"to before from", "from=2026-08-02T00:00:00Z&to=2026-08-01T00:00:00Z&bucket=hour"},
		{"to equals from", "from=2026-08-01T00:00:00Z&to=2026-08-01T00:00:00Z&bucket=hour"},
		// A month+1 hour of hourly buckets exceeds the 744 cap.
		{"over cap", "from=2026-01-01T00:00:00Z&to=2026-02-01T01:00:00Z&bucket=hour"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer(true)
			srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
			incidents := &fakeIncidents{}
			wireIncidentTrends(srv, incidents)

			req := httptest.NewRequest(http.MethodGet, "/v1/admin/dashboards/incident-trends?"+tc.query, nil)
			req.Header.Set("Authorization", "Bearer "+adminIncidentTrendsToken(t))
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("status = %d, want 422: %s", rec.Code, rec.Body.String())
			}
			if incidents.trendsCalls != 0 {
				t.Error("a rejected request must never reach the store")
			}
		})
	}
}

// TestIncidentTrends_AtTheCap_IsAccepted proves the boundary itself (exactly
// 744 hourly buckets) is served, not refused — the cap test above only
// proves the over-cap side.
func TestIncidentTrends_AtTheCap_IsAccepted(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	incidents := &fakeIncidents{}
	wireIncidentTrends(srv, incidents)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/admin/dashboards/incident-trends?from=2026-01-01T00:00:00Z&to=2026-02-01T00:00:00Z&bucket=hour", nil)
	req.Header.Set("Authorization", "Bearer "+adminIncidentTrendsToken(t))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got incidentTrendsResponseDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Points) != domain.MaxIncidentTrendBuckets {
		t.Errorf("points = %d, want exactly the cap %d", len(got.Points), domain.MaxIncidentTrendBuckets)
	}
}

// ---------------------------------------------------------- series assembly / zero-fill

// TestBuildIncidentTrendsResponse_ZeroFillsEmptyBuckets is the pure
// assembly/zero-fill unit test the review criteria call for: a 4-hour window
// with data in only bucket 0 and 3 still returns 4 contiguous points, 1 and
// 2 zeroed.
func TestBuildIncidentTrendsResponse_ZeroFillsEmptyBuckets(t *testing.T) {
	from := mustParseRFC3339HTTP(t, "2026-08-01T00:00:00Z")
	to := mustParseRFC3339HTTP(t, "2026-08-01T04:00:00Z")
	q := domain.IncidentTrendsQuery{From: from, To: to, Bucket: domain.IncidentTrendBucketHour}

	opened := []domain.IncidentOpenedTrendPoint{
		{BucketStart: from, Severity: domain.IncidentSeverityCritical, Source: domain.IncidentSourceManual, Count: 2},
		{BucketStart: from.Add(3 * time.Hour), Severity: domain.IncidentSeverityLow, Source: domain.IncidentSourceAlert, Count: 5},
	}
	resolved := []domain.IncidentResolvedTrendPoint{
		{BucketStart: from.Add(3 * time.Hour), Count: 1},
	}

	got := buildIncidentTrendsResponse(q, opened, resolved)
	if len(got.Points) != 4 {
		t.Fatalf("points = %d, want 4", len(got.Points))
	}
	if got.Points[0].OpenedTotal != 2 || got.Points[0].OpenedBySeverity.Critical != 2 || got.Points[0].OpenedBySource.Manual != 2 {
		t.Errorf("bucket[0] = %+v, want opened_total=2 critical=2 manual=2", got.Points[0])
	}
	for i := 1; i <= 2; i++ {
		p := got.Points[i]
		if p.OpenedTotal != 0 || p.ResolvedTotal != 0 {
			t.Errorf("bucket[%d] = %+v, want a zeroed bucket (no data)", i, p)
		}
	}
	if got.Points[3].OpenedTotal != 5 || got.Points[3].OpenedBySeverity.Low != 5 || got.Points[3].OpenedBySource.Alert != 5 {
		t.Errorf("bucket[3] = %+v, want opened_total=5 low=5 alert=5", got.Points[3])
	}
	if got.Points[3].ResolvedTotal != 1 {
		t.Errorf("bucket[3].resolved_total = %d, want 1", got.Points[3].ResolvedTotal)
	}
	// bucket_start values must be exactly the contiguous, boundary-aligned
	// shape from.BucketStarts() produces — the same series regardless of
	// what data happened to exist.
	for i, want := range q.BucketStarts() {
		if !got.Points[i].BucketStart.Equal(want) {
			t.Errorf("bucket[%d].bucket_start = %v, want %v", i, got.Points[i].BucketStart, want)
		}
	}
}

// TestBuildIncidentTrendsResponse_SeverityAndSourceAggregateAcrossRows proves
// two opened rows landing in the SAME bucket (different severity/source
// combinations) both accumulate into that one bucket's totals rather than
// overwriting each other.
func TestBuildIncidentTrendsResponse_SeverityAndSourceAggregateAcrossRows(t *testing.T) {
	from := mustParseRFC3339HTTP(t, "2026-08-01T00:00:00Z")
	to := mustParseRFC3339HTTP(t, "2026-08-01T01:00:00Z")
	q := domain.IncidentTrendsQuery{From: from, To: to, Bucket: domain.IncidentTrendBucketHour}

	opened := []domain.IncidentOpenedTrendPoint{
		{BucketStart: from, Severity: domain.IncidentSeverityCritical, Source: domain.IncidentSourceManual, Count: 3},
		{BucketStart: from, Severity: domain.IncidentSeverityHigh, Source: domain.IncidentSourceAlert, Count: 4},
		{BucketStart: from, Severity: domain.IncidentSeverityMedium, Source: domain.IncidentSourceManual, Count: 1},
		{BucketStart: from, Severity: domain.IncidentSeverityLow, Source: domain.IncidentSourceAlert, Count: 2},
	}

	got := buildIncidentTrendsResponse(q, opened, nil)
	if len(got.Points) != 1 {
		t.Fatalf("points = %d, want 1", len(got.Points))
	}
	p := got.Points[0]
	if p.OpenedTotal != 10 {
		t.Errorf("opened_total = %d, want 10", p.OpenedTotal)
	}
	if p.OpenedBySeverity.Critical != 3 || p.OpenedBySeverity.High != 4 || p.OpenedBySeverity.Medium != 1 || p.OpenedBySeverity.Low != 2 {
		t.Errorf("opened_by_severity = %+v", p.OpenedBySeverity)
	}
	if p.OpenedBySource.Manual != 4 || p.OpenedBySource.Alert != 6 {
		t.Errorf("opened_by_source = %+v, want manual=4 alert=6", p.OpenedBySource)
	}
}

// TestBuildIncidentTrendsResponse_DropsRowsOutsideTheGeneratedShape proves a
// defensive property: a store row whose bucket_start does not match any
// generated slot (which q.Validate should make impossible in production) is
// silently dropped rather than panicking.
func TestBuildIncidentTrendsResponse_DropsRowsOutsideTheGeneratedShape(t *testing.T) {
	from := mustParseRFC3339HTTP(t, "2026-08-01T00:00:00Z")
	to := mustParseRFC3339HTTP(t, "2026-08-01T01:00:00Z")
	q := domain.IncidentTrendsQuery{From: from, To: to, Bucket: domain.IncidentTrendBucketHour}

	opened := []domain.IncidentOpenedTrendPoint{
		{BucketStart: from.Add(48 * time.Hour), Severity: domain.IncidentSeverityCritical, Source: domain.IncidentSourceManual, Count: 99},
	}
	got := buildIncidentTrendsResponse(q, opened, nil)
	if len(got.Points) != 1 || got.Points[0].OpenedTotal != 0 {
		t.Errorf("out-of-shape row must not appear: %+v", got.Points)
	}
}

// TestIncidentTrends_EmptyWindow_ReturnsAllZeroSeries is the end-to-end
// empty-window proof: a valid window with no matching incidents at all still
// returns a full, contiguous, all-zero series (never an empty points array).
func TestIncidentTrends_EmptyWindow_ReturnsAllZeroSeries(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	incidents := &fakeIncidents{} // trendsOpened/trendsResolved both nil
	wireIncidentTrends(srv, incidents)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/admin/dashboards/incident-trends?from=2026-08-01T00:00:00Z&to=2026-08-01T06:00:00Z&bucket=hour", nil)
	req.Header.Set("Authorization", "Bearer "+adminIncidentTrendsToken(t))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got incidentTrendsResponseDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Points) != 6 {
		t.Fatalf("points = %d, want 6 contiguous zeroed buckets", len(got.Points))
	}
	for i, p := range got.Points {
		if p.OpenedTotal != 0 || p.ResolvedTotal != 0 || p.OpenedBySeverity != (incidentTrendSeverityCountsDTO{}) ||
			p.OpenedBySource != (incidentTrendSourceCountsDTO{}) {
			t.Errorf("bucket[%d] = %+v, want all-zero", i, p)
		}
	}
	if incidents.trendsCalls != 1 {
		t.Errorf("store calls = %d, want 1", incidents.trendsCalls)
	}
}

// TestIncidentTrends_PassesTheParsedQueryToTheStore proves the handler
// forwards exactly what it parsed (UTC-normalised) to the store, not a
// re-derived value.
func TestIncidentTrends_PassesTheParsedQueryToTheStore(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	incidents := &fakeIncidents{}
	wireIncidentTrends(srv, incidents)

	req := httptest.NewRequest(http.MethodGet,
		"/v1/admin/dashboards/incident-trends?from=2026-08-01T00:00:00Z&to=2026-08-02T00:00:00Z&bucket=day", nil)
	req.Header.Set("Authorization", "Bearer "+adminIncidentTrendsToken(t))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if incidents.lastTrendsQuery.Bucket != domain.IncidentTrendBucketDay {
		t.Errorf("bucket forwarded = %q, want day", incidents.lastTrendsQuery.Bucket)
	}
	if !incidents.lastTrendsQuery.From.Equal(mustParseRFC3339HTTP(t, "2026-08-01T00:00:00Z")) {
		t.Errorf("from forwarded = %v", incidents.lastTrendsQuery.From)
	}
	if !incidents.lastTrendsQuery.To.Equal(mustParseRFC3339HTTP(t, "2026-08-02T00:00:00Z")) {
		t.Errorf("to forwarded = %v", incidents.lastTrendsQuery.To)
	}
}

func mustParseRFC3339HTTP(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return ts
}
