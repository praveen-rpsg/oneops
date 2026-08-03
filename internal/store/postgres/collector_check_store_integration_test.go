//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func TestCollectorCheckStore_CreateGetListUpdateDelete(t *testing.T) {
	testPool(t) // ensures migrations are applied before the scoped pool is used
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "collector-check-crud")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	checks := NewCollectorCheckStore(scoped)
	ctx := assetTestCtx(tn)

	host := telemetryAsset(t, assets, ctx, tn.TenantID, "collector-host")

	c, err := domain.NewCollectorCheck(tn.TenantID, host.AssetID, domain.CollectorCheckTypeHTTP, "https://example.test/health", 60, "chk")
	if err != nil {
		t.Fatalf("new collector check: %v", err)
	}
	created, err := checks.Create(ctx, c)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RowVersion != 1 {
		t.Errorf("row_version = %d, want 1", created.RowVersion)
	}
	if created.LastRunAt != nil || created.LastStatus != "" {
		t.Errorf("a new check must never have run: %+v", created)
	}

	got, err := checks.Get(ctx, created.CheckID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Target != "https://example.test/health" || got.MetricPrefix != "chk" {
		t.Errorf("get returned %+v, want the created fields", got)
	}

	list, err := checks.List(ctx, 0, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, item := range list {
		if item.CheckID == created.CheckID {
			found = true
		}
	}
	if !found {
		t.Errorf("list did not include the created check: %+v", list)
	}

	newTarget := "https://example.test/healthz"
	updated, err := checks.Update(ctx, created.CheckID, created.RowVersion, domain.CollectorCheckPatch{Target: &newTarget})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Target != newTarget || updated.RowVersion != 2 {
		t.Errorf("update = %+v, want target %q and row_version 2", updated, newTarget)
	}

	// A stale row_version is refused.
	if _, err := checks.Update(ctx, created.CheckID, created.RowVersion, domain.CollectorCheckPatch{Target: &newTarget}); err != domain.ErrVersionMismatch {
		t.Errorf("stale update err = %v, want ErrVersionMismatch", err)
	}

	if err := checks.Delete(ctx, created.CheckID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := checks.Get(ctx, created.CheckID); err != domain.ErrNotFound {
		t.Errorf("get after delete err = %v, want ErrNotFound", err)
	}
	if err := checks.Delete(ctx, created.CheckID); err != domain.ErrNotFound {
		t.Errorf("delete of an already-deleted check err = %v, want ErrNotFound", err)
	}
}

// TestCollectorCheckStore_CreateRejectsCrossTenantOrNonexistentAsset proves
// ADR-ASSET-001 §6's defense extended to collector_check: a check cannot be
// created against another tenant's asset, or against an asset_id that does
// not exist anywhere — both return ErrNotFound, not a cross-tenant row.
func TestCollectorCheckStore_CreateRejectsCrossTenantOrNonexistentAsset(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("cc-rej-alpha", "ext-cc-rej-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("cc-rej-bravo", "ext-cc-rej-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	checks := NewCollectorCheckStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	victim := telemetryAsset(t, assets, ctxA, a.TenantID, "victim-host")

	// Tenant B names tenant A's real asset.
	cross, err := domain.NewCollectorCheck(b.TenantID, victim.AssetID, domain.CollectorCheckTypeHTTP, "https://example.test", 60, "chk")
	if err != nil {
		t.Fatalf("new collector check: %v", err)
	}
	if _, err := checks.Create(ctxB, cross); err != domain.ErrNotFound {
		t.Errorf("cross-tenant asset_id err = %v, want ErrNotFound", err)
	}

	// Tenant B names an asset id that does not exist anywhere.
	missing, err := domain.NewCollectorCheck(b.TenantID, "no-such-asset", domain.CollectorCheckTypeHTTP, "https://example.test", 60, "chk")
	if err != nil {
		t.Fatalf("new collector check: %v", err)
	}
	if _, err := checks.Create(ctxB, missing); err != domain.ErrNotFound {
		t.Errorf("nonexistent asset_id err = %v, want ErrNotFound", err)
	}

	// Nothing was written naming the victim's asset.
	list, err := checks.List(ctxA, 0, "")
	if err != nil {
		t.Fatalf("list as tenant a: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a cross-tenant check was written: %+v", list)
	}
}

// TestCollectorCheckIsolation_RLSByTenant proves two-tenant isolation bites:
// tenant B can never see tenant A's checks, even by the exact check_id.
func TestCollectorCheckIsolation_RLSByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("cc-iso-alpha", "ext-cc-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("cc-iso-bravo", "ext-cc-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	checks := NewCollectorCheckStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	hostA := telemetryAsset(t, assets, ctxA, a.TenantID, "iso-host-a")

	cA, err := domain.NewCollectorCheck(a.TenantID, hostA.AssetID, domain.CollectorCheckTypeHTTP, "https://example.test", 60, "chk")
	if err != nil {
		t.Fatalf("new collector check: %v", err)
	}
	createdA, err := checks.Create(ctxA, cA)
	if err != nil {
		t.Fatalf("create as tenant a: %v", err)
	}

	if _, err := checks.Get(ctxB, createdA.CheckID); err != domain.ErrNotFound {
		t.Errorf("tenant B read tenant A's check by id: err = %v, want ErrNotFound", err)
	}
	listB, err := checks.List(ctxB, 0, "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	if len(listB) != 0 {
		t.Errorf("tenant B saw tenant A's checks: %+v", listB)
	}
	if err := checks.Delete(ctxB, createdA.CheckID); err != domain.ErrNotFound {
		t.Errorf("tenant B deleted tenant A's check: err = %v, want ErrNotFound", err)
	}

	// Tenant A still sees its own check, undisturbed.
	stillThere, err := checks.Get(ctxA, createdA.CheckID)
	if err != nil || stillThere.CheckID != createdA.CheckID {
		t.Fatalf("tenant A lost its own check: %v, %+v", err, stillThere)
	}
}

// TestCollectorCheckStore_DueChecksRespectsEnabledAndInterval exercises the
// scheduler's due-scan directly, over the PRIVILEGED pool exactly as the
// real scheduler runs it: never-run is due; run recently with a long
// interval is not due; run long ago with a short interval is due again;
// disabled is never due regardless of timing.
func TestCollectorCheckStore_DueChecksRespectsEnabledAndInterval(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "collector-check-due")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	scopedChecks := NewCollectorCheckStore(scoped)
	ctx := assetTestCtx(tn)
	host := telemetryAsset(t, assets, ctx, tn.TenantID, "due-host")

	mk := func(interval int, enabled bool) *domain.CollectorCheck {
		c, err := domain.NewCollectorCheck(tn.TenantID, host.AssetID, domain.CollectorCheckTypeHTTP, "https://example.test", interval, "chk")
		if err != nil {
			t.Fatalf("new collector check: %v", err)
		}
		c.Enabled = enabled
		created, err := scopedChecks.Create(ctx, c)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return created
	}

	neverRun := mk(60, true)
	disabled := mk(30, false)
	longInterval := mk(3600, true)
	shortIntervalStale := mk(30, true)

	privChecks := NewCollectorCheckStore(priv)
	now := time.Now().UTC()

	// Prime last_run_at on the two "already ran" fixtures.
	if err := privChecks.RecordRun(context.Background(), longInterval.CheckID, now.Add(-time.Minute), domain.CollectorCheckStatusOK); err != nil {
		t.Fatalf("record run (long interval): %v", err)
	}
	if err := privChecks.RecordRun(context.Background(), shortIntervalStale.CheckID, now.Add(-time.Hour), domain.CollectorCheckStatusOK); err != nil {
		t.Fatalf("record run (short interval stale): %v", err)
	}

	due, err := privChecks.DueChecks(context.Background(), now, 100)
	if err != nil {
		t.Fatalf("due checks: %v", err)
	}
	dueIDs := map[string]bool{}
	for _, c := range due {
		dueIDs[c.CheckID] = true
	}
	if !dueIDs[neverRun.CheckID] {
		t.Error("a check that has never run must be due")
	}
	if dueIDs[disabled.CheckID] {
		t.Error("a disabled check must never be due")
	}
	if dueIDs[longInterval.CheckID] {
		t.Error("a check run 1 minute ago with a 1-hour interval must not be due yet")
	}
	if !dueIDs[shortIntervalStale.CheckID] {
		t.Error("a check run 1 hour ago with a 30-second interval must be due again")
	}
}

// TestCollectorCheckStore_RecordRunDoesNotBumpRowVersion proves the design
// decision collector_check's migration header records: the scheduler's
// background write must never consume the optimistic-lock version an
// operator's own PATCH depends on.
func TestCollectorCheckStore_RecordRunDoesNotBumpRowVersion(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "collector-check-recordrun")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	checks := NewCollectorCheckStore(scoped)
	ctx := assetTestCtx(tn)
	host := telemetryAsset(t, assets, ctx, tn.TenantID, "recordrun-host")

	c, err := domain.NewCollectorCheck(tn.TenantID, host.AssetID, domain.CollectorCheckTypeHTTP, "https://example.test", 60, "chk")
	if err != nil {
		t.Fatalf("new collector check: %v", err)
	}
	created, err := checks.Create(ctx, c)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	privChecks := NewCollectorCheckStore(priv)
	runAt := time.Now().UTC()
	if err := privChecks.RecordRun(context.Background(), created.CheckID, runAt, domain.CollectorCheckStatusOK); err != nil {
		t.Fatalf("record run: %v", err)
	}

	got, err := checks.Get(ctx, created.CheckID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RowVersion != created.RowVersion {
		t.Errorf("row_version changed from %d to %d after RecordRun, want unchanged", created.RowVersion, got.RowVersion)
	}
	if got.LastRunAt == nil || !got.LastRunAt.Equal(runAt) {
		t.Errorf("last_run_at = %v, want %v", got.LastRunAt, runAt)
	}
	if got.LastStatus != domain.CollectorCheckStatusOK {
		t.Errorf("last_status = %q, want ok", got.LastStatus)
	}

	// The operator's PATCH, built from the row_version it read before
	// RecordRun ran, still succeeds — it was never invalidated.
	newTarget := "https://example.test/v2"
	if _, err := checks.Update(ctx, created.CheckID, created.RowVersion, domain.CollectorCheckPatch{Target: &newTarget}); err != nil {
		t.Fatalf("update using the pre-RecordRun row_version: %v", err)
	}
}
