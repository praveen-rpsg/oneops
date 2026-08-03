//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// telemetryAsset creates a real asset on the given tenant-scoped store, for
// a telemetry test to point samples at — mirrors assetTenant's fixture shape.
func telemetryAsset(t *testing.T, assets *AssetStore, ctx context.Context, tenantID, name string) *domain.Asset {
	t.Helper()
	a, err := domain.NewAsset(tenantID, "server", name, nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	created, err := assets.Create(ctx, a)
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	return created
}

func TestTelemetryStore_IngestQueryRoundTrip(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "telemetry-roundtrip")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewTelemetryStore(scoped)
	ctx := assetTestCtx(tn)

	host := telemetryAsset(t, assets, ctx, tn.TenantID, "host-1")

	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	samples := []domain.Sample{
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "cpu_utilization", Value: 10, Timestamp: base, Labels: map[string]string{"host": "h1"}},
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "cpu_utilization", Value: 20, Timestamp: base.Add(1 * time.Minute)},
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "cpu_utilization", Value: 30, Timestamp: base.Add(2 * time.Minute)},
		// A different metric on the same asset must not appear in a
		// cpu_utilization range query.
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "disk_free_bytes", Value: 999, Timestamp: base},
	}
	results, err := store.WriteSamples(ctx, samples)
	if err != nil {
		t.Fatalf("write samples: %v", err)
	}
	if len(results) != len(samples) {
		t.Fatalf("results = %d, want %d", len(results), len(samples))
	}
	for i, r := range results {
		if !r.Accepted {
			t.Errorf("sample %d rejected: %s", i, r.Reason)
		}
	}

	got, err := store.QueryRange(ctx, host.AssetID, "cpu_utilization", base, base.Add(10*time.Minute), 0, time.Time{}, domain.ResolutionAuto)
	if err != nil {
		t.Fatalf("query range: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d samples, want 3: %+v", len(got), got)
	}
	for i, want := range []float64{10, 20, 30} {
		if got[i].Value != want {
			t.Errorf("sample %d value = %v, want %v", i, got[i].Value, want)
		}
	}
	if got[0].Labels["host"] != "h1" {
		t.Errorf("labels did not round-trip: %+v", got[0].Labels)
	}

	// Re-sending the identical batch is idempotent: still reported accepted,
	// and no new rows appear.
	if _, err := store.WriteSamples(ctx, samples[:3]); err != nil {
		t.Fatalf("re-write samples: %v", err)
	}
	again, err := store.QueryRange(ctx, host.AssetID, "cpu_utilization", base, base.Add(10*time.Minute), 0, time.Time{}, domain.ResolutionAuto)
	if err != nil {
		t.Fatalf("query range after re-write: %v", err)
	}
	if len(again) != 3 {
		t.Fatalf("re-import created duplicate rows: got %d, want 3", len(again))
	}

	// Keyset pagination: after the first sample's timestamp, only the
	// remaining two are returned.
	page, err := store.QueryRange(ctx, host.AssetID, "cpu_utilization", base, base.Add(10*time.Minute), 0, base, domain.ResolutionAuto)
	if err != nil {
		t.Fatalf("query range with after: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("paginated query = %d samples, want 2: %+v", len(page), page)
	}
}

func TestTelemetryStore_RejectsCrossTenantOrNonexistentAsset(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("telemetry-rej-alpha", "ext-telemetry-rej-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("telemetry-rej-bravo", "ext-telemetry-rej-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewTelemetryStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	victim := telemetryAsset(t, assets, ctxA, a.TenantID, "victim-host")

	ts := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	results, err := store.WriteSamples(ctxB, []domain.Sample{
		// Tenant B names tenant A's real asset.
		{TenantID: b.TenantID, AssetID: victim.AssetID, Metric: "cpu_utilization", Value: 1, Timestamp: ts},
		// Tenant B names an asset id that does not exist anywhere.
		{TenantID: b.TenantID, AssetID: "no-such-asset", Metric: "cpu_utilization", Value: 2, Timestamp: ts},
	})
	if err != nil {
		t.Fatalf("write samples: %v", err)
	}
	for i, r := range results {
		if r.Accepted {
			t.Errorf("sample %d was accepted, want rejected: %+v", i, r)
		}
		if r.Reason == "" {
			t.Errorf("sample %d rejected with no reason", i)
		}
	}

	// No row was written naming the victim's asset — checked from tenant A's
	// own connection, where it would be visible if it existed.
	gotA, err := store.QueryRange(ctxA, victim.AssetID, "cpu_utilization", ts.Add(-time.Hour), ts.Add(time.Hour), 0, time.Time{}, domain.ResolutionAuto)
	if err != nil {
		t.Fatalf("query range as victim tenant: %v", err)
	}
	if len(gotA) != 0 {
		t.Fatalf("a cross-tenant sample was written: %+v", gotA)
	}
}

func TestTelemetryIsolation_RLSByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("telemetry-iso-alpha", "ext-telemetry-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("telemetry-iso-bravo", "ext-telemetry-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewTelemetryStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	hostA := telemetryAsset(t, assets, ctxA, a.TenantID, "host-a")
	hostB := telemetryAsset(t, assets, ctxB, b.TenantID, "host-b")

	ts := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	if _, err := store.WriteSamples(ctxA, []domain.Sample{
		{TenantID: a.TenantID, AssetID: hostA.AssetID, Metric: "cpu_utilization", Value: 111, Timestamp: ts},
	}); err != nil {
		t.Fatalf("write sample as tenant a: %v", err)
	}
	if _, err := store.WriteSamples(ctxB, []domain.Sample{
		{TenantID: b.TenantID, AssetID: hostB.AssetID, Metric: "cpu_utilization", Value: 222, Timestamp: ts},
	}); err != nil {
		t.Fatalf("write sample as tenant b: %v", err)
	}

	// Tenant B can never see tenant A's sample, even naming tenant A's own
	// asset_id and metric directly — row-level security filters on tenant_id,
	// not on the caller's chosen filters.
	gotB, err := store.QueryRange(ctxB, hostA.AssetID, "cpu_utilization", ts.Add(-time.Hour), ts.Add(time.Hour), 0, time.Time{}, domain.ResolutionAuto)
	if err != nil {
		t.Fatalf("query range as tenant b: %v", err)
	}
	if len(gotB) != 0 {
		t.Fatalf("tenant B saw tenant A's telemetry: %+v", gotB)
	}

	gotA, err := store.QueryRange(ctxA, hostA.AssetID, "cpu_utilization", ts.Add(-time.Hour), ts.Add(time.Hour), 0, time.Time{}, domain.ResolutionAuto)
	if err != nil {
		t.Fatalf("query range as tenant a: %v", err)
	}
	if len(gotA) != 1 || gotA[0].Value != 111 {
		t.Fatalf("tenant A did not see its own telemetry: %+v", gotA)
	}
}

// TestTelemetrySample_IsARealHypertable proves the D1 storage decision
// (ADR-TELEMETRY-001) landed, not merely a plain table with the right name:
// telemetry_sample must be registered in TimescaleDB's own catalogue.
func TestTelemetrySample_IsARealHypertable(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	var dimensions int
	err := pool.QueryRow(ctx, `
		SELECT num_dimensions FROM timescaledb_information.hypertables
		 WHERE hypertable_name = 'telemetry_sample' AND hypertable_schema = current_schema()`,
	).Scan(&dimensions)
	if err != nil {
		t.Fatalf("telemetry_sample is not registered as a TimescaleDB hypertable: %v", err)
	}
	if dimensions < 1 {
		t.Errorf("telemetry_sample hypertable has %d dimensions, want at least 1", dimensions)
	}
}
