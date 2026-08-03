//go:build integration

package postgres

import (
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// TestTelemetryRetentionWorker_DropsOldRawChunks proves retention actually
// bounds growth (verified, not asserted): a sample far older than the
// retention horizon is gone after one retention pass, chunk-and-all, while a
// recent sample in a different chunk survives.
//
// The two samples are placed 200 days apart specifically so they land in
// different TimescaleDB chunks (the default chunk interval is 7 days):
// drop_chunks only removes a chunk whose ENTIRE range is older than the
// cutoff, so a fixture with both samples in the same chunk would prove
// nothing — the chunk would be retained regardless of the cutoff because
// "now" is still inside its range.
func TestTelemetryRetentionWorker_DropsOldRawChunks(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "telemetry-retention")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewTelemetryStore(scoped)
	ctx := assetTestCtx(tn)
	host := telemetryAsset(t, assets, ctx, tn.TenantID, "retention-host")

	now := time.Now().UTC()
	oldTS := now.AddDate(0, 0, -200)
	recentTS := now.Add(-time.Hour)

	if _, err := store.WriteSamples(ctx, []domain.Sample{
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "cpu_utilization", Value: 1, Timestamp: oldTS},
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "cpu_utilization", Value: 2, Timestamp: recentTS},
	}); err != nil {
		t.Fatalf("write samples: %v", err)
	}

	// Each point is checked with its OWN narrow window and an EXPLICIT
	// ResolutionRaw: a single [oldTS, now] window would be wider than
	// domain.AutoResolutionRawWindow (and even than MaxRawQueryWindow), so
	// ResolutionAuto — or an explicit raw request — would either read the
	// (here, empty) rollup or be rejected outright. Neither would test
	// retention; querying each point in its own narrow window does.
	oldWindow := func() ([]domain.Sample, error) {
		return store.QueryRange(ctx, host.AssetID, "cpu_utilization",
			oldTS.Add(-time.Hour), oldTS.Add(time.Hour), 0, time.Time{}, domain.ResolutionRaw)
	}
	recentWindow := func() ([]domain.Sample, error) {
		return store.QueryRange(ctx, host.AssetID, "cpu_utilization",
			recentTS.Add(-time.Hour), recentTS.Add(time.Hour), 0, time.Time{}, domain.ResolutionRaw)
	}

	beforeOld, err := oldWindow()
	if err != nil {
		t.Fatalf("query old window before retention: %v", err)
	}
	beforeRecent, err := recentWindow()
	if err != nil {
		t.Fatalf("query recent window before retention: %v", err)
	}
	if len(beforeOld) != 1 || len(beforeRecent) != 1 {
		t.Fatalf("fixture: got %d old / %d recent samples before retention, want 1 each",
			len(beforeOld), len(beforeRecent))
	}

	worker := NewTelemetryRetentionWorker(priv, quietRollupLogger(), TelemetryRetentionConfig{})
	worker.dropOldChunks(adminTestCtx(), "telemetry_sample", domain.DefaultTelemetryRawRetentionDays)

	afterOld, err := oldWindow()
	if err != nil {
		t.Fatalf("query old window after retention: %v", err)
	}
	if len(afterOld) != 0 {
		t.Errorf("the old sample survived retention: %+v", afterOld)
	}
	afterRecent, err := recentWindow()
	if err != nil {
		t.Fatalf("query recent window after retention: %v", err)
	}
	if len(afterRecent) != 1 || afterRecent[0].Timestamp.Unix() != recentTS.Unix() {
		t.Errorf("the recent sample did not survive retention: %+v", afterRecent)
	}
}

// TestTelemetryRetentionWorker_DropsOldRollupChunks is the same proof for the
// rollup's own, longer, fixed retention.
func TestTelemetryRetentionWorker_DropsOldRollupChunks(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "telemetry-retention-rollup")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	store := NewTelemetryStore(scoped)
	ctx := assetTestCtx(tn)
	host := telemetryAsset(t, assets, ctx, tn.TenantID, "retention-rollup-host")

	now := time.Now().UTC()
	oldBucket := now.AddDate(-2, 0, 0) // 2 years back: well past any chunk near "now"
	recentBucket := now.Add(-time.Hour).Truncate(5 * time.Minute)

	if _, err := store.WriteSamples(ctx, []domain.Sample{
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "cpu_utilization", Value: 1, Timestamp: oldBucket},
		{TenantID: tn.TenantID, AssetID: host.AssetID, Metric: "cpu_utilization", Value: 2, Timestamp: recentBucket},
	}); err != nil {
		t.Fatalf("write samples: %v", err)
	}

	rollupWorker := NewTelemetryRollupWorker(priv, quietRollupLogger(), TelemetryRollupConfig{})
	if _, err := rollupWorker.MaterializeRange(adminTestCtx(), oldBucket.Add(-time.Minute), now.Add(time.Minute)); err != nil {
		t.Fatalf("materialize range: %v", err)
	}

	before, err := store.QueryRange(ctx, host.AssetID, "cpu_utilization",
		oldBucket.Add(-time.Hour), now.Add(time.Hour), 0, time.Time{}, domain.ResolutionRollup5m)
	if err != nil {
		t.Fatalf("query rollup before retention: %v", err)
	}
	if len(before) != 2 {
		t.Fatalf("fixture: got %d rollup rows before retention, want 2: %+v", len(before), before)
	}

	retentionWorker := NewTelemetryRetentionWorker(priv, quietRollupLogger(), TelemetryRetentionConfig{})
	retentionWorker.dropOldChunks(adminTestCtx(), "telemetry_rollup_5m", domain.DefaultTelemetryRollupRetentionDays)

	after, err := store.QueryRange(ctx, host.AssetID, "cpu_utilization",
		oldBucket.Add(-time.Hour), now.Add(time.Hour), 0, time.Time{}, domain.ResolutionRollup5m)
	if err != nil {
		t.Fatalf("query rollup after retention: %v", err)
	}
	if len(after) != 1 {
		t.Fatalf("got %d rollup rows after retention, want 1 (the recent one): %+v", len(after), after)
	}
}

// TestTelemetryRetentionWorker_EffectiveRawRetentionDaysReadsSystemTenant
// proves the operator-tunable half of E2.1b: a value stored under
// SystemTenantID changes what the worker resolves; the registry default
// governs when nothing is stored.
func TestTelemetryRetentionWorker_EffectiveRawRetentionDaysReadsSystemTenant(t *testing.T) {
	priv := testPool(t)
	worker := NewTelemetryRetentionWorker(priv, quietRollupLogger(), TelemetryRetentionConfig{})

	got, err := worker.effectiveRawRetentionDays(adminTestCtx())
	if err != nil {
		t.Fatalf("effective raw retention days (default): %v", err)
	}
	if got != domain.DefaultTelemetryRawRetentionDays {
		t.Errorf("with no stored override, got %d, want the default %d", got, domain.DefaultTelemetryRawRetentionDays)
	}

	settings := NewSettingStore(priv)
	st, err := domain.NewSetting(domain.SystemTenantID, domain.TelemetryRawRetentionDaysKey, "30", 0)
	if err != nil {
		t.Fatalf("new setting: %v", err)
	}
	if _, err := settings.Upsert(adminTestCtx(), st); err != nil {
		t.Fatalf("upsert setting: %v", err)
	}

	got, err = worker.effectiveRawRetentionDays(adminTestCtx())
	if err != nil {
		t.Fatalf("effective raw retention days (overridden): %v", err)
	}
	if got != 30 {
		t.Errorf("with a system-tenant override of 30, got %d", got)
	}
}
