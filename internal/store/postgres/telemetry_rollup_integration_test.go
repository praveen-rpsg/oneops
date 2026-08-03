//go:build integration

package postgres

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func quietRollupLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(logDiscard{}, nil))
}

// TestTelemetryRollupWorker_MaterializesAvgMinMaxCount proves the downsampling
// half of E2.1b: five raw samples across two 5-minute buckets roll up to the
// correct avg/min/max/count per bucket, read back through
// QueryRange(resolution=rollup_5m).
func TestTelemetryRollupWorker_MaterializesAvgMinMaxCount(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "telemetry-rollup-avg")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewTelemetryStore(scoped)
	ctx := assetTestCtx(tn)

	host := telemetryAsset(t, assets, ctx, tn.TenantID, "rollup-host")

	bucket0 := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	bucket1 := bucket0.Add(5 * time.Minute)

	samples := []domain.Sample{
		// bucket0: 10, 20, 30 -> avg 20, min 10, max 30, count 3
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "cpu_utilization", Value: 10, Timestamp: bucket0},
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "cpu_utilization", Value: 20, Timestamp: bucket0.Add(time.Minute)},
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "cpu_utilization", Value: 30, Timestamp: bucket0.Add(2 * time.Minute)},
		// bucket1: 100, 200 -> avg 150, min 100, max 200, count 2
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "cpu_utilization", Value: 100, Timestamp: bucket1},
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "cpu_utilization", Value: 200, Timestamp: bucket1.Add(time.Minute)},
	}
	if _, err := store.WriteSamples(ctx, samples); err != nil {
		t.Fatalf("write samples: %v", err)
	}

	worker := NewTelemetryRollupWorker(priv, quietRollupLogger(), TelemetryRollupConfig{})
	n, err := worker.MaterializeRange(context.Background(), bucket0, bucket1.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("materialize range: %v", err)
	}
	if n != 2 {
		t.Fatalf("materialized %d buckets, want 2", n)
	}

	got, err := store.QueryRange(ctx, host.AssetID, "cpu_utilization",
		bucket0, bucket1.Add(10*time.Minute), 0, time.Time{}, domain.ResolutionRollup5m)
	if err != nil {
		t.Fatalf("query rollup range: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rollup rows, want 2: %+v", len(got), got)
	}

	first, second := got[0], got[1]
	if !first.Timestamp.Equal(bucket0) {
		t.Errorf("bucket 0 timestamp = %v, want %v", first.Timestamp, bucket0)
	}
	if first.Value != 20 || first.Min == nil || *first.Min != 10 || first.Max == nil || *first.Max != 30 ||
		first.Count == nil || *first.Count != 3 {
		t.Errorf("bucket 0 = avg %v min %v max %v count %v, want avg 20 min 10 max 30 count 3",
			first.Value, deref(first.Min), deref(first.Max), derefI(first.Count))
	}
	if !second.Timestamp.Equal(bucket1) {
		t.Errorf("bucket 1 timestamp = %v, want %v", second.Timestamp, bucket1)
	}
	if second.Value != 150 || second.Min == nil || *second.Min != 100 || second.Max == nil || *second.Max != 200 ||
		second.Count == nil || *second.Count != 2 {
		t.Errorf("bucket 1 = avg %v min %v max %v count %v, want avg 150 min 100 max 200 count 2",
			second.Value, deref(second.Min), deref(second.Max), derefI(second.Count))
	}

	// Re-materializing the identical range is idempotent: still exactly two
	// rows, same values, not four.
	if _, err := worker.MaterializeRange(context.Background(), bucket0, bucket1.Add(5*time.Minute)); err != nil {
		t.Fatalf("re-materialize range: %v", err)
	}
	again, err := store.QueryRange(ctx, host.AssetID, "cpu_utilization",
		bucket0, bucket1.Add(10*time.Minute), 0, time.Time{}, domain.ResolutionRollup5m)
	if err != nil {
		t.Fatalf("query rollup range after re-materialize: %v", err)
	}
	if len(again) != 2 {
		t.Fatalf("re-materializing duplicated rollup rows: got %d, want 2", len(again))
	}
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func derefI(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}

// TestTelemetryRollupIsolation_RLSByTenant is the mutation-critical proof for
// E2.1b: telemetry_rollup_5m is NOT a Timescale continuous aggregate (whose
// backing materialization is a separate object never proven to carry RLS —
// exactly the risk this test exists to close) but a table this platform
// owns outright, carrying the identical ENABLE/FORCE ROW LEVEL SECURITY +
// tenant_isolation policy telemetry_sample does. Mirrors
// TestTelemetryIsolation_RLSByTenant exactly, for the rollup read path.
//
// This test BITES: it would fail immediately if telemetry_rollup_5m's RLS
// policy, ENABLE, or FORCE were ever weakened or removed — tenant B naming
// tenant A's own asset_id and metric directly would then see tenant A's
// rolled-up averages, exactly the cross-tenant aggregate leak the story
// calls a serious breach.
func TestTelemetryRollupIsolation_RLSByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("telemetry-rollup-iso-alpha", "ext-telemetry-rollup-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("telemetry-rollup-iso-bravo", "ext-telemetry-rollup-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewTelemetryStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	hostA := telemetryAsset(t, assets, ctxA, a.TenantID, "rollup-host-a")
	hostB := telemetryAsset(t, assets, ctxB, b.TenantID, "rollup-host-b")

	bucket := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	if _, err := store.WriteSamples(ctxA, []domain.Sample{
		{TenantID: a.TenantID, AssetID: hostA.AssetID, Metric: "cpu_utilization", Value: 111, Timestamp: bucket},
	}); err != nil {
		t.Fatalf("write sample as tenant a: %v", err)
	}
	if _, err := store.WriteSamples(ctxB, []domain.Sample{
		{TenantID: b.TenantID, AssetID: hostB.AssetID, Metric: "cpu_utilization", Value: 222, Timestamp: bucket},
	}); err != nil {
		t.Fatalf("write sample as tenant b: %v", err)
	}

	// Materialize on the PRIVILEGED pool, exactly as the real worker does —
	// this writes both tenants' rollup rows in one cross-tenant pass, which
	// is precisely the shape that would leak if RLS did not confine the read
	// side afterward.
	worker := NewTelemetryRollupWorker(priv, quietRollupLogger(), TelemetryRollupConfig{})
	if _, err := worker.MaterializeRange(context.Background(), bucket, bucket.Add(5*time.Minute)); err != nil {
		t.Fatalf("materialize range: %v", err)
	}

	// Tenant B must never see tenant A's rollup, even naming tenant A's own
	// asset_id and metric directly.
	gotB, err := store.QueryRange(ctxB, hostA.AssetID, "cpu_utilization",
		bucket.Add(-time.Hour), bucket.Add(time.Hour), 0, time.Time{}, domain.ResolutionRollup5m)
	if err != nil {
		t.Fatalf("query rollup as tenant b: %v", err)
	}
	if len(gotB) != 0 {
		t.Fatalf("tenant B saw tenant A's rollup: %+v", gotB)
	}

	gotA, err := store.QueryRange(ctxA, hostA.AssetID, "cpu_utilization",
		bucket.Add(-time.Hour), bucket.Add(time.Hour), 0, time.Time{}, domain.ResolutionRollup5m)
	if err != nil {
		t.Fatalf("query rollup as tenant a: %v", err)
	}
	if len(gotA) != 1 || gotA[0].Value != 111 {
		t.Fatalf("tenant A did not see its own rollup: %+v", gotA)
	}
}

// TestTelemetryRollup_IsARealHypertable proves telemetry_rollup_5m is a
// TimescaleDB hypertable, not merely a plain table with the right name — the
// same D1-shaped proof TestTelemetrySample_IsARealHypertable gives for
// telemetry_sample.
func TestTelemetryRollup_IsARealHypertable(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	var dimensions int
	err := pool.QueryRow(ctx, `
		SELECT num_dimensions FROM timescaledb_information.hypertables
		 WHERE hypertable_name = 'telemetry_rollup_5m' AND hypertable_schema = current_schema()`,
	).Scan(&dimensions)
	if err != nil {
		t.Fatalf("telemetry_rollup_5m is not registered as a TimescaleDB hypertable: %v", err)
	}
	if dimensions < 1 {
		t.Errorf("telemetry_rollup_5m hypertable has %d dimensions, want at least 1", dimensions)
	}
}

// TestTelemetryQueryRange_ResolutionSelection proves the resolution/rollup
// selection E2.1b requires: a narrow window defaults to raw, a wide window
// defaults to the rollup, and an explicit "raw" request over a very wide
// window is rejected rather than forcing an unbounded-in-spirit raw scan.
func TestTelemetryQueryRange_ResolutionSelection(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "telemetry-resolution")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewTelemetryStore(scoped)
	ctx := assetTestCtx(tn)
	host := telemetryAsset(t, assets, ctx, tn.TenantID, "resolution-host")

	base := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	if _, err := store.WriteSamples(ctx, []domain.Sample{
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "cpu_utilization", Value: 10, Timestamp: base},
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "cpu_utilization", Value: 20, Timestamp: base.Add(time.Minute)},
	}); err != nil {
		t.Fatalf("write samples: %v", err)
	}
	worker := NewTelemetryRollupWorker(priv, quietRollupLogger(), TelemetryRollupConfig{})
	if _, err := worker.MaterializeRange(context.Background(), base, base.Add(10*time.Minute)); err != nil {
		t.Fatalf("materialize range: %v", err)
	}

	// Narrow window (well under AutoResolutionRawWindow): auto picks raw —
	// individual samples, no Min/Max/Count.
	narrow, err := store.QueryRange(ctx, host.AssetID, "cpu_utilization",
		base, base.Add(10*time.Minute), 0, time.Time{}, domain.ResolutionAuto)
	if err != nil {
		t.Fatalf("narrow auto query: %v", err)
	}
	if len(narrow) != 2 {
		t.Fatalf("narrow auto query returned %d rows, want 2 raw samples: %+v", len(narrow), narrow)
	}
	for _, s := range narrow {
		if s.Min != nil || s.Max != nil || s.Count != nil {
			t.Errorf("narrow auto query returned a rollup-shaped row, want raw: %+v", s)
		}
	}

	// Wide window (beyond AutoResolutionRawWindow): auto picks the rollup —
	// one bucket, Min/Max/Count populated.
	wide, err := store.QueryRange(ctx, host.AssetID, "cpu_utilization",
		base.Add(-domain.AutoResolutionRawWindow), base.Add(domain.AutoResolutionRawWindow), 0, time.Time{}, domain.ResolutionAuto)
	if err != nil {
		t.Fatalf("wide auto query: %v", err)
	}
	if len(wide) == 0 {
		t.Fatal("wide auto query returned no rows")
	}
	for _, s := range wide {
		if s.Min == nil || s.Max == nil || s.Count == nil {
			t.Errorf("wide auto query returned a raw-shaped row, want rollup: %+v", s)
		}
	}

	// An EXPLICIT raw request over a window wider than MaxRawQueryWindow is
	// rejected — a caller cannot force a full year of per-sample rows through
	// this path just by asking for resolution=raw.
	_, err = store.QueryRange(ctx, host.AssetID, "cpu_utilization",
		base.Add(-domain.MaxRawQueryWindow), base.Add(domain.MaxRawQueryWindow), 0, time.Time{}, domain.ResolutionRaw)
	if err == nil {
		t.Fatal("explicit raw over MaxRawQueryWindow was accepted, want a validation error")
	}
	if _, ok := domain.AsValidation(err); !ok {
		t.Errorf("error is not a domain.ValidationError: %v", err)
	}
}
