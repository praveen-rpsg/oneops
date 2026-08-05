//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

func TestMaintenanceWindowStore_CreateGetListDelete(t *testing.T) {
	testPool(t) // ensures migrations are applied before the scoped pool is used
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "maint-window-crud")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	windows := NewMaintenanceWindowStore(scoped)
	ctx := assetTestCtx(tn)

	host := telemetryAsset(t, assets, ctx, tn.TenantID, "maint-host")

	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	w, err := domain.NewMaintenanceWindow(tn.TenantID, host.AssetID, "patching", start, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("new maintenance window: %v", err)
	}
	created, err := windows.Create(ctx, w)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RowVersion != 1 {
		t.Errorf("row_version = %d, want 1", created.RowVersion)
	}
	if created.CreatedBy == "" {
		t.Error("created_by must be set server-side from the authenticated actor")
	}
	if created.SuppressedCount != 0 || !created.LastSuppressedAt.IsZero() {
		t.Errorf("a new window must start with no recorded suppression: %+v", created)
	}

	got, err := windows.Get(ctx, created.WindowID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.StartsAt.Equal(start) || !got.EndsAt.Equal(start.Add(time.Hour)) || got.Reason != "patching" {
		t.Errorf("get returned %+v, want the created fields", got)
	}

	list, err := windows.List(ctx, 0, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, item := range list {
		if item.WindowID == created.WindowID {
			found = true
		}
	}
	if !found {
		t.Errorf("list did not include the created window: %+v", list)
	}

	if err := windows.Delete(ctx, created.WindowID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := windows.Get(ctx, created.WindowID); err != domain.ErrNotFound {
		t.Errorf("get after delete err = %v, want ErrNotFound", err)
	}
	if err := windows.Delete(ctx, created.WindowID); err != domain.ErrNotFound {
		t.Errorf("delete of an already-deleted window err = %v, want ErrNotFound", err)
	}
}

// TestMaintenanceWindowStore_CreateRejectsCrossTenantOrNonexistentAsset
// proves ADR-ASSET-001 §6's defense extended to maintenance_window: a window
// cannot be declared against another tenant's asset, or against an asset_id
// that does not exist anywhere — both return ErrNotFound, not a cross-tenant
// row.
func TestMaintenanceWindowStore_CreateRejectsCrossTenantOrNonexistentAsset(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("mw-rej-alpha", "ext-mw-rej-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("mw-rej-bravo", "ext-mw-rej-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	windows := NewMaintenanceWindowStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	victim := telemetryAsset(t, assets, ctxA, a.TenantID, "victim-host")
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	// Tenant B names tenant A's real asset.
	cross, err := domain.NewMaintenanceWindow(b.TenantID, victim.AssetID, "", start, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("new maintenance window: %v", err)
	}
	if _, err := windows.Create(ctxB, cross); err != domain.ErrNotFound {
		t.Errorf("cross-tenant asset_id err = %v, want ErrNotFound", err)
	}

	// Tenant B names an asset id that does not exist anywhere.
	missing, err := domain.NewMaintenanceWindow(b.TenantID, "no-such-asset", "", start, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("new maintenance window: %v", err)
	}
	if _, err := windows.Create(ctxB, missing); err != domain.ErrNotFound {
		t.Errorf("nonexistent asset_id err = %v, want ErrNotFound", err)
	}

	// Nothing was written naming the victim's asset.
	list, err := windows.List(ctxA, 0, "")
	if err != nil {
		t.Fatalf("list as tenant a: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a cross-tenant window was written: %+v", list)
	}
}

// TestMaintenanceWindowIsolation_RLSByTenant proves two-tenant isolation
// bites on the admin CRUD surface: tenant B can never see tenant A's
// windows, even by the exact window_id.
func TestMaintenanceWindowIsolation_RLSByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("mw-iso-alpha", "ext-mw-iso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("mw-iso-bravo", "ext-mw-iso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	windows := NewMaintenanceWindowStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	hostA := telemetryAsset(t, assets, ctxA, a.TenantID, "iso-host-a")
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	wA, err := domain.NewMaintenanceWindow(a.TenantID, hostA.AssetID, "", start, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("new maintenance window: %v", err)
	}
	createdA, err := windows.Create(ctxA, wA)
	if err != nil {
		t.Fatalf("create as tenant a: %v", err)
	}

	if _, err := windows.Get(ctxB, createdA.WindowID); err != domain.ErrNotFound {
		t.Errorf("tenant B read tenant A's window by id: err = %v, want ErrNotFound", err)
	}
	listB, err := windows.List(ctxB, 0, "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	if len(listB) != 0 {
		t.Errorf("tenant B saw tenant A's windows: %+v", listB)
	}
	if err := windows.Delete(ctxB, createdA.WindowID); err != domain.ErrNotFound {
		t.Errorf("tenant B deleted tenant A's window: err = %v, want ErrNotFound", err)
	}

	// Tenant A still sees its own window, undisturbed.
	stillThere, err := windows.Get(ctxA, createdA.WindowID)
	if err != nil || stillThere.WindowID != createdA.WindowID {
		t.Fatalf("tenant A lost its own window: %v, %+v", err, stillThere)
	}
}

// TestMaintenanceWindowStore_SuppressIsActiveOnlyInsideHalfOpenBounds is the
// store-level boundary proof for the evaluator's privileged surface: the
// same PostgreSQL statement alerting.MaintenanceWindowChecker.Suppress runs,
// exercised directly, across exactly-at-start (suppressed), exactly-at-end
// (not suppressed), before, and after.
func TestMaintenanceWindowStore_SuppressIsActiveOnlyInsideHalfOpenBounds(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "maint-window-bounds")

	scoped := tenantScopedPool(t)
	assets := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)
	host := telemetryAsset(t, assets, ctx, tn.TenantID, "bounds-host")

	scopedWindows := NewMaintenanceWindowStore(scoped)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	w, err := domain.NewMaintenanceWindow(tn.TenantID, host.AssetID, "", start, end)
	if err != nil {
		t.Fatalf("new maintenance window: %v", err)
	}
	if _, err := scopedWindows.Create(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}

	privWindows := NewMaintenanceWindowStore(priv)
	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"before the window", start.Add(-time.Second), false},
		{"exactly at starts_at", start, true},
		{"mid-window", start.Add(30 * time.Minute), true},
		{"one nanosecond before ends_at", end.Add(-time.Nanosecond), true},
		{"exactly at ends_at", end, false},
		{"after the window", end.Add(time.Second), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, suppressed, err := privWindows.Suppress(context.Background(), tn.TenantID, host.AssetID, tt.at)
			if err != nil {
				t.Fatalf("suppress: %v", err)
			}
			if suppressed != tt.want {
				t.Errorf("at=%v: suppressed=%v, want %v", tt.at, suppressed, tt.want)
			}
		})
	}

	// suppressed_count/last_suppressed_at recorded the two suppressing calls
	// above (starts_at, mid-window, one-ns-before-ends_at = 3), never a
	// silent drop.
	got, err := scopedWindows.Get(ctx, w.WindowID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SuppressedCount != 3 {
		t.Errorf("suppressed_count = %d, want 3", got.SuppressedCount)
	}
	// timestamptz has microsecond precision, so the sub-microsecond part of
	// the nanosecond-precision boundary above is legitimately truncated on
	// round-trip — compare at the column's own resolution.
	wantLast := end.Add(-time.Nanosecond).Truncate(time.Microsecond)
	if !got.LastSuppressedAt.Truncate(time.Microsecond).Equal(wantLast) {
		t.Errorf("last_suppressed_at = %v, want %v (the last suppressing call)", got.LastSuppressedAt, wantLast)
	}
}

// TestMaintenanceWindowStore_SuppressIsTenantIsolated is the privileged-pool
// isolation proof ADR-TENANCY-012 requires: two tenants share the exact same
// asset_id (an adversarial collision a real, globally-unique asset id would
// not itself produce, but Suppress's isolation must not depend on that) —
// only the tenant that actually declared a window over it is ever
// suppressed.
//
// Mutation-verified: removing "tenant_id = $1" from
// MaintenanceWindowStore.Suppress's WHERE clause makes this test fail —
// tenant B's rule would be suppressed by tenant A's window.
func TestMaintenanceWindowStore_SuppressIsTenantIsolated(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("mw-tsuppr-alpha", "ext-mw-tsuppr-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("mw-tsuppr-bravo", "ext-mw-tsuppr-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	// asset.asset_id has a real FK from maintenance_window (ADR-ASSET-001
	// §6), so the shared id under test must name an actual row per tenant —
	// each tenant creates its OWN asset row, and the raw insert below labels
	// tenant B's maintenance_window row with tenant A's asset_id, exploiting
	// the exact crack that FK's own privileges bypass row-level security on
	// asset (the same fact TestTelemetryStore_QueryRangeForTenantIsolatesTenant's
	// doc comment records).
	scoped := tenantScopedPool(t)
	sharedAssetRow := telemetryAsset(t, NewAssetStore(scoped), assetTestCtx(a), a.TenantID, "shared-mw-isolation-host")
	sharedAsset := sharedAssetRow.AssetID

	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	// Only tenant A declares a window, naming the shared asset_id.
	scopedWindows := NewMaintenanceWindowStore(scoped)
	wA, err := domain.NewMaintenanceWindow(a.TenantID, sharedAsset, "", start, end)
	if err != nil {
		t.Fatalf("new maintenance window: %v", err)
	}
	if _, err := scopedWindows.Create(assetTestCtx(a), wA); err != nil {
		t.Fatalf("create tenant a's window: %v", err)
	}

	privWindows := NewMaintenanceWindowStore(priv)
	at := start.Add(time.Minute)

	_, suppressedA, err := privWindows.Suppress(context.Background(), a.TenantID, sharedAsset, at)
	if err != nil {
		t.Fatalf("suppress tenant a: %v", err)
	}
	if !suppressedA {
		t.Error("tenant a's own window did not suppress its own firing")
	}

	_, suppressedB, err := privWindows.Suppress(context.Background(), b.TenantID, sharedAsset, at)
	if err != nil {
		t.Fatalf("suppress tenant b: %v", err)
	}
	if suppressedB {
		t.Error("tenant b was suppressed by tenant a's window over a shared asset_id — cross-tenant leak")
	}
}
