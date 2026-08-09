//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/store/migrate"
	"github.com/rpsg/oneops/internal/store/postgres"
)

// incidentTrendsHarness wires GET /admin/dashboards/incident-trends against
// the REAL IncidentStore — a tenant-scoped appPool (row-level security
// enforced, exactly as production's composition root builds it) plus a
// privileged pool used ONLY for tenant registration, never for anything the
// trends endpoint itself reads. This is the wiring the handler unit tests
// (fakes) cannot prove: that RLS, not a predicate the query happens to
// carry, is what isolates one tenant's series from another's (ADR-NOC-008,
// mirrors ADR-NOC-006's own asset-graph harness).
type incidentTrendsHarness struct {
	router    http.Handler
	dsn       string
	priv      *pgxpool.Pool
	appPool   *pgxpool.Pool
	incidents *postgres.IncidentStore
	assets    *postgres.AssetStore
	tenants   *postgres.TenantStore
}

func realIncidentTrendsHarness(t *testing.T) *incidentTrendsHarness {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	dsn := base + sep + "options=-c%20search_path%3Dhttpapi_itest"
	ctx := context.Background()

	priv, err := postgres.NewPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("privileged pool: %v", err)
	}
	var pingErr error
	for i := 0; i < 60; i++ {
		if pingErr = priv.Ping(ctx); pingErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if pingErr != nil {
		t.Fatalf("db not ready: %v", pingErr)
	}
	if _, err := priv.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS httpapi_itest`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := migrate.Up(ctx, priv); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(priv.Close)

	appPool, err := postgres.NewTenantScopedPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("tenant-scoped pool: %v", err)
	}
	t.Cleanup(appPool.Close)

	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), priv.Ping)

	h := &incidentTrendsHarness{
		dsn: dsn, priv: priv, appPool: appPool,
		incidents: postgres.NewIncidentStore(appPool),
		assets:    postgres.NewAssetStore(appPool),
		tenants:   postgres.NewTenantStore(priv),
	}
	s.SetTenants(h.tenants)
	s.SetIncidents(h.incidents)
	h.router = s.Router()
	return h
}

// trendsTenant registers a real tenant on the privileged pool.
func (h *incidentTrendsHarness) trendsTenant(t *testing.T, slug string) *domain.Tenant {
	t.Helper()
	slug = uniqueSlug(slug)
	tn, err := h.tenants.Create(domain.WithActor(context.Background(), "test-actor"), &domain.Tenant{
		TenantID: domain.NewID(), Slug: slug, Name: slug, ExternalID: "ext-" + slug,
		Status: domain.TenantActive,
	})
	if err != nil {
		t.Fatalf("create tenant %s: %v", slug, err)
	}
	return tn
}

func (h *incidentTrendsHarness) ctx(tn *domain.Tenant) context.Context {
	return domain.WithActor(domain.WithTenant(context.Background(), tn), "test-actor")
}

// seedManualIncident creates a manual, unlinked incident through the real
// write path (IncidentStore.Create on the tenant-scoped pool, exercising RLS'
// WITH CHECK exactly as a production write would), then backdates its
// created_at/resolved_at to at/resolvedAt directly on the same tenant-scoped
// connection — the same "seed through the real path, then backdate the
// derived timestamp with a raw UPDATE" pattern
// asset_health_integration_test.go's backdateLastSeen already establishes.
// resolvedAt nil leaves resolved_at NULL (the incident is not resolved).
func (h *incidentTrendsHarness) seedManualIncident(
	t *testing.T, tn *domain.Tenant, severity domain.IncidentSeverity, at time.Time, resolvedAt *time.Time,
) *domain.Incident {
	t.Helper()
	inc, err := domain.NewIncident(tn.TenantID, "trend-fixture", "", severity, nil, nil)
	if err != nil {
		t.Fatalf("new incident: %v", err)
	}
	created, err := h.incidents.Create(h.ctx(tn), inc)
	if err != nil {
		t.Fatalf("create incident: %v", err)
	}
	h.backdate(t, tn, created.IncidentID, at, resolvedAt)
	return created
}

// seedAlertIncident is seedManualIncident's alert-sourced counterpart: an
// alert-sourced incident always names an asset (domain.NewAlertIncident), so
// this first creates one through the real AssetStore write path.
func (h *incidentTrendsHarness) seedAlertIncident(
	t *testing.T, tn *domain.Tenant, severity domain.IncidentSeverity, at time.Time, resolvedAt *time.Time,
) *domain.Incident {
	t.Helper()
	asset, err := domain.NewAsset(tn.TenantID, "server", "trend-fixture-host-"+domain.NewID(), nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	createdAsset, err := h.assets.Create(h.ctx(tn), asset)
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	inc, err := domain.NewAlertIncident(tn.TenantID, "trend-fixture-alert", "", severity, createdAsset.AssetID)
	if err != nil {
		t.Fatalf("new alert incident: %v", err)
	}
	created, err := h.incidents.Create(h.ctx(tn), inc)
	if err != nil {
		t.Fatalf("create alert incident: %v", err)
	}
	h.backdate(t, tn, created.IncidentID, at, resolvedAt)
	return created
}

// seedSecurityIncident is seedAlertIncident's security-sourced counterpart
// (E8.1b-2, domain.NewSecurityIncident) — E9.3's fix proves this source
// lands in its own opened_by_source bucket rather than being dropped.
func (h *incidentTrendsHarness) seedSecurityIncident(
	t *testing.T, tn *domain.Tenant, severity domain.IncidentSeverity, at time.Time, resolvedAt *time.Time,
) *domain.Incident {
	t.Helper()
	asset, err := domain.NewAsset(tn.TenantID, "server", "trend-fixture-host-"+domain.NewID(), nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	createdAsset, err := h.assets.Create(h.ctx(tn), asset)
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	inc, err := domain.NewSecurityIncident(tn.TenantID, "trend-fixture-security", "", severity, createdAsset.AssetID)
	if err != nil {
		t.Fatalf("new security incident: %v", err)
	}
	created, err := h.incidents.Create(h.ctx(tn), inc)
	if err != nil {
		t.Fatalf("create security incident: %v", err)
	}
	h.backdate(t, tn, created.IncidentID, at, resolvedAt)
	return created
}

// seedVulnIncident is seedAlertIncident's vuln-sourced counterpart (E8.3b,
// domain.NewVulnRemediationIncident) — E9.3's fix proves this source lands
// in its own opened_by_source bucket rather than being dropped.
func (h *incidentTrendsHarness) seedVulnIncident(
	t *testing.T, tn *domain.Tenant, severity domain.IncidentSeverity, at time.Time, resolvedAt *time.Time,
) *domain.Incident {
	t.Helper()
	asset, err := domain.NewAsset(tn.TenantID, "server", "trend-fixture-host-"+domain.NewID(), nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	createdAsset, err := h.assets.Create(h.ctx(tn), asset)
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	inc, err := domain.NewVulnRemediationIncident(tn.TenantID, "trend-fixture-vuln", "", severity, createdAsset.AssetID)
	if err != nil {
		t.Fatalf("new vuln incident: %v", err)
	}
	created, err := h.incidents.Create(h.ctx(tn), inc)
	if err != nil {
		t.Fatalf("create vuln incident: %v", err)
	}
	h.backdate(t, tn, created.IncidentID, at, resolvedAt)
	return created
}

// backdate sets incidentID's created_at (and, if non-nil, resolved_at)
// directly on the tenant-scoped connection — RLS' WITH CHECK still applies
// (the connection remains bound to tn), it is only the derived timestamp
// this bypasses, not the tenant boundary.
func (h *incidentTrendsHarness) backdate(t *testing.T, tn *domain.Tenant, incidentID string, at time.Time, resolvedAt *time.Time) {
	t.Helper()
	if _, err := h.appPool.Exec(h.ctx(tn),
		`UPDATE incident SET created_at = $2 WHERE incident_id = $1`, incidentID, at); err != nil {
		t.Fatalf("backdate created_at for %s: %v", incidentID, err)
	}
	if resolvedAt != nil {
		if _, err := h.appPool.Exec(h.ctx(tn),
			`UPDATE incident SET resolved_at = $2 WHERE incident_id = $1`, incidentID, *resolvedAt); err != nil {
			t.Fatalf("backdate resolved_at for %s: %v", incidentID, err)
		}
	}
}

// trendsGet issues an authenticated GET /v1/admin/dashboards/incident-trends
// as tn's admin against whatever router r serves (so the "bites" test can
// swap in a deliberately loosened one).
func trendsGet(t *testing.T, r http.Handler, tn *domain.Tenant, from, to time.Time, bucket domain.IncidentTrendBucket) (*httptest.ResponseRecorder, incidentTrendsResponseDTO) {
	t.Helper()
	url := "/v1/admin/dashboards/incident-trends?from=" + from.UTC().Format(time.RFC3339) +
		"&to=" + to.UTC().Format(time.RFC3339) + "&bucket=" + string(bucket)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, tn.ExternalID, []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var got incidentTrendsResponseDTO
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode trends: %v (body %s)", err, rec.Body.String())
		}
	}
	return rec, got
}

func (h *incidentTrendsHarness) trendsGetOwn(t *testing.T, tn *domain.Tenant, from, to time.Time, bucket domain.IncidentTrendBucket) (*httptest.ResponseRecorder, incidentTrendsResponseDTO) {
	return trendsGet(t, h.router, tn, from, to, bucket)
}

// pointAt finds the point whose bucket_start equals want, failing the test if
// it is missing — every response must be contiguous, so a bucket that should
// exist but does not is itself a failure worth a clear message.
func pointAt(t *testing.T, points []incidentTrendPointDTO, want time.Time) incidentTrendPointDTO {
	t.Helper()
	for _, p := range points {
		if p.BucketStart.Equal(want) {
			return p
		}
	}
	t.Fatalf("no bucket at %v in %d points", want, len(points))
	return incidentTrendPointDTO{}
}

// TestIncidentTrendsAPI_BucketsBySeverityAndSourceAndZeroFills is the
// correctness proof: incidents seeded across three hourly buckets, several
// severities and all four sources (manual/alert/security/vuln, E9.3),
// produce exact per-bucket counts, and a bucket with no incidents at all
// (the middle one) still appears, zeroed — the contiguous, zero-filled
// series the review criteria require.
func TestIncidentTrendsAPI_BucketsBySeverityAndSourceAndZeroFills(t *testing.T) {
	h := realIncidentTrendsHarness(t)
	tn := h.trendsTenant(t, "trends-correctness")

	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(3 * time.Hour)

	// Bucket 0 [00:00,01:00): two manual critical, one alert high, one
	// security medium, one vuln low.
	h.seedManualIncident(t, tn, domain.IncidentSeverityCritical, from.Add(10*time.Minute), nil)
	h.seedManualIncident(t, tn, domain.IncidentSeverityCritical, from.Add(20*time.Minute), nil)
	h.seedAlertIncident(t, tn, domain.IncidentSeverityHigh, from.Add(30*time.Minute), nil)
	h.seedSecurityIncident(t, tn, domain.IncidentSeverityMedium, from.Add(35*time.Minute), nil)
	h.seedVulnIncident(t, tn, domain.IncidentSeverityLow, from.Add(40*time.Minute), nil)
	// Bucket 1 [01:00,02:00): deliberately empty — proves zero-fill.
	// Bucket 2 [02:00,03:00): one manual low.
	h.seedManualIncident(t, tn, domain.IncidentSeverityLow, from.Add(2*time.Hour+5*time.Minute), nil)
	// Outside the window entirely — must never be counted.
	h.seedManualIncident(t, tn, domain.IncidentSeverityCritical, from.Add(-time.Hour), nil)
	h.seedManualIncident(t, tn, domain.IncidentSeverityCritical, to.Add(time.Hour), nil)

	rec, got := h.trendsGetOwn(t, tn, from, to, domain.IncidentTrendBucketHour)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(got.Points) != 3 {
		t.Fatalf("points = %d, want 3 contiguous buckets: %+v", len(got.Points), got.Points)
	}

	b0 := pointAt(t, got.Points, from)
	if b0.OpenedTotal != 5 || b0.OpenedBySeverity.Critical != 2 || b0.OpenedBySeverity.High != 1 ||
		b0.OpenedBySeverity.Medium != 1 || b0.OpenedBySeverity.Low != 1 {
		t.Errorf("bucket0 = %+v, want opened_total=5 critical=2 high=1 medium=1 low=1", b0)
	}
	wantSource := incidentTrendSourceCountsDTO{Manual: 2, Alert: 1, Security: 1, Vuln: 1}
	if b0.OpenedBySource != wantSource {
		t.Errorf("bucket0.opened_by_source = %+v, want %+v", b0.OpenedBySource, wantSource)
	}

	b1 := pointAt(t, got.Points, from.Add(time.Hour))
	if b1.OpenedTotal != 0 {
		t.Errorf("bucket1 (deliberately empty) = %+v, want opened_total=0", b1)
	}

	b2 := pointAt(t, got.Points, from.Add(2*time.Hour))
	if b2.OpenedTotal != 1 || b2.OpenedBySeverity.Low != 1 || b2.OpenedBySource.Manual != 1 {
		t.Errorf("bucket2 = %+v, want opened_total=1 low=1 manual=1", b2)
	}
}

// TestIncidentTrendsAPI_ResolvedCountsAreBucketedByResolvedAtNotCreatedAt
// proves the resolved series uses its own timestamp column: an incident
// created in bucket 0 but resolved in bucket 2 contributes to bucket 0's
// opened_total and bucket 2's resolved_total, never the other way round.
func TestIncidentTrendsAPI_ResolvedCountsAreBucketedByResolvedAtNotCreatedAt(t *testing.T) {
	h := realIncidentTrendsHarness(t)
	tn := h.trendsTenant(t, "trends-resolved")

	from := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	to := from.Add(3 * time.Hour)
	resolvedAt := from.Add(2*time.Hour + 15*time.Minute)

	h.seedManualIncident(t, tn, domain.IncidentSeverityHigh, from.Add(5*time.Minute), &resolvedAt)
	// A second incident, opened AND resolved in bucket 1, so bucket 1 has a
	// non-zero opened_total alongside its own resolved_total.
	midResolved := from.Add(time.Hour + 40*time.Minute)
	h.seedManualIncident(t, tn, domain.IncidentSeverityMedium, from.Add(time.Hour+10*time.Minute), &midResolved)

	rec, got := h.trendsGetOwn(t, tn, from, to, domain.IncidentTrendBucketHour)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	b0 := pointAt(t, got.Points, from)
	if b0.OpenedTotal != 1 || b0.ResolvedTotal != 0 {
		t.Errorf("bucket0 = %+v, want opened_total=1 resolved_total=0 (resolved in bucket 2, not here)", b0)
	}
	b1 := pointAt(t, got.Points, from.Add(time.Hour))
	if b1.OpenedTotal != 1 || b1.ResolvedTotal != 1 {
		t.Errorf("bucket1 = %+v, want opened_total=1 resolved_total=1", b1)
	}
	b2 := pointAt(t, got.Points, from.Add(2*time.Hour))
	if b2.OpenedTotal != 0 || b2.ResolvedTotal != 1 {
		t.Errorf("bucket2 = %+v, want opened_total=0 resolved_total=1 (the first incident's resolution)", b2)
	}
}

// TestIncidentTrendsAPI_EmptyTenantIsAllZeroSeries proves the empty-window
// bound at the real-store layer: a brand-new tenant with no incidents at all
// gets 200 with a full, contiguous, all-zero series — never an empty points
// array, never a 500.
func TestIncidentTrendsAPI_EmptyTenantIsAllZeroSeries(t *testing.T) {
	h := realIncidentTrendsHarness(t)
	tn := h.trendsTenant(t, "trends-empty")

	from := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	to := from.Add(4 * time.Hour)

	rec, got := h.trendsGetOwn(t, tn, from, to, domain.IncidentTrendBucketHour)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(got.Points) != 4 {
		t.Fatalf("points = %d, want 4", len(got.Points))
	}
	for _, p := range got.Points {
		if p.OpenedTotal != 0 || p.ResolvedTotal != 0 {
			t.Errorf("bucket %+v, want all-zero for an empty tenant", p)
		}
	}
}

// TestIncidentTrendsAPI_TenantIsolation is the security-critical property
// this story exists to prove: two tenants with materially different
// incident volumes, sharing no rows, each see ONLY their own trends — not
// the other's counts, not the sum of both. A third, larger bystander tenant
// proves A's own count is not merely coincidental.
func TestIncidentTrendsAPI_TenantIsolation(t *testing.T) {
	h := realIncidentTrendsHarness(t)
	tnA := h.trendsTenant(t, "trends-iso-a")
	tnB := h.trendsTenant(t, "trends-iso-b")
	tnC := h.trendsTenant(t, "trends-iso-c")

	from := time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	h.seedManualIncident(t, tnA, domain.IncidentSeverityCritical, from.Add(5*time.Minute), nil)
	h.seedManualIncident(t, tnA, domain.IncidentSeverityCritical, from.Add(10*time.Minute), nil)

	h.seedManualIncident(t, tnB, domain.IncidentSeverityLow, from.Add(5*time.Minute), nil)
	h.seedManualIncident(t, tnB, domain.IncidentSeverityLow, from.Add(10*time.Minute), nil)
	h.seedManualIncident(t, tnB, domain.IncidentSeverityLow, from.Add(15*time.Minute), nil)

	// A larger bystander tenant, so A's own count is not just "everyone's".
	for i := 0; i < 6; i++ {
		h.seedManualIncident(t, tnC, domain.IncidentSeverityHigh, from.Add(5*time.Minute), nil)
	}

	_, gotA := h.trendsGetOwn(t, tnA, from, to, domain.IncidentTrendBucketHour)
	_, gotB := h.trendsGetOwn(t, tnB, from, to, domain.IncidentTrendBucketHour)

	if len(gotA.Points) != 1 || gotA.Points[0].OpenedTotal != 2 || gotA.Points[0].OpenedBySeverity.Critical != 2 {
		t.Fatalf("tenant A trends = %+v, want exactly 2 critical, never B's or C's", gotA.Points)
	}
	if len(gotB.Points) != 1 || gotB.Points[0].OpenedTotal != 3 || gotB.Points[0].OpenedBySeverity.Low != 3 {
		t.Fatalf("tenant B trends = %+v, want exactly 3 low, never A's or C's", gotB.Points)
	}
}

// TestIncidentTrendsAPI_TenantIsolation_BitesWhenLoosened is the mutation
// proof the review criteria require: re-run the exact isolation fixture
// against a router deliberately wired with IncidentStore built over the
// PRIVILEGED (RLS-bypassing) pool instead of the tenant-scoped one, and
// require the isolation assertion to FAIL there. If it did not fail, the
// "real" test above would be proving nothing (mirrors
// TestAssetGraphAPI_TenantIsolation_BitesWhenLoosened, ADR-NOC-006 §2).
func TestIncidentTrendsAPI_TenantIsolation_BitesWhenLoosened(t *testing.T) {
	h := realIncidentTrendsHarness(t)
	tnA := h.trendsTenant(t, "trends-bites-a")
	tnB := h.trendsTenant(t, "trends-bites-b")

	from := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	// Tenant A uses ONLY critical; tenant B uses ONLY low — a severity A
	// never creates anywhere in this window. This is a PRESENCE check, not
	// an exact-count one, deliberately: this harness reuses a persistent
	// database across test runs (only CREATE SCHEMA IF NOT EXISTS, never a
	// reset), so an exact total at a fixed calendar timestamp would
	// accumulate across repeated invocations of this very test. A nonzero
	// "low" count in tenant A's own read is unambiguous leakage regardless
	// of how many prior runs contributed to it — the same reasoning
	// TestAssetGraphAPI_TenantIsolation_BitesWhenLoosened's own name-presence
	// check (rather than a node-count one) already relies on.
	h.seedManualIncident(t, tnA, domain.IncidentSeverityCritical, from.Add(5*time.Minute), nil)
	h.seedManualIncident(t, tnB, domain.IncidentSeverityLow, from.Add(5*time.Minute), nil)

	// Wire a SECOND server whose IncidentStore is built over the privileged
	// pool (postgres.NewPool, table-owner/BYPASSRLS — no app.tenant_id
	// binding at all) — the exact mistake ADR-TENANCY-002 exists to catch.
	loosePool, err := postgres.NewPool(context.Background(), h.dsn, 3)
	if err != nil {
		t.Fatalf("privileged pool for loosened router: %v", err)
	}
	t.Cleanup(loosePool.Close)

	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	loose := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), loosePool.Ping)
	loose.SetTenants(h.tenants)
	loose.SetIncidents(postgres.NewIncidentStore(loosePool))
	looseRouter := loose.Router()

	_, gotA := trendsGet(t, looseRouter, tnA, from, to, domain.IncidentTrendBucketHour)
	if len(gotA.Points) != 1 || gotA.Points[0].OpenedBySeverity.Low == 0 {
		t.Fatalf("expected the LOOSENED router (privileged pool, no tenant scoping) to leak tenant B's "+
			"low-severity incident into tenant A's trends (opened_by_severity.low > 0); got %+v, which means "+
			"this canary no longer proves the real harness's isolation is load-bearing", gotA.Points)
	}
}
