//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

// backdateLastSeen sets assetID's last_seen directly, on the tenant-scoped
// connection, to simulate a CI a source confirmed a long time ago (at=nil
// simulates "never confirmed" — NULL). AssetStore itself never exposes a way
// to set an arbitrary LastSeen (it is always the write's own "now" — see
// domain.Asset.LastSeen's doc comment), so a test that needs an OLD or NULL
// one must reach past the repository, exactly as
// TestAsset_SourceExternalRefUniqueness_IsEnforcedByTheDatabase reaches past
// Upsert with a raw INSERT.
func backdateLastSeen(t *testing.T, ctx context.Context, scoped *pgxpool.Pool, assetID string, at *time.Time) {
	t.Helper()
	if _, err := scoped.Exec(ctx, `UPDATE asset SET last_seen = $2 WHERE asset_id = $1`, assetID, at); err != nil {
		t.Fatalf("backdate last_seen for %s: %v", assetID, err)
	}
}

// newHealthAsset builds and creates an active asset with the given
// environment/criticality/owner classification, for the health-report tests
// below — a thin wrapper over NewAsset + ApplyClassification + Create so each
// test can say exactly which classification axis it is exercising.
func newHealthAsset(
	t *testing.T, ctx context.Context, store *AssetStore, tenantID, assetType, name, environment, criticality string, ownerTeamID *string,
) *domain.Asset {
	t.Helper()
	a, err := domain.NewAsset(tenantID, assetType, name, nil)
	if err != nil {
		t.Fatalf("new asset %s: %v", name, err)
	}
	if err := a.ApplyClassification(environment, criticality, ownerTeamID, nil); err != nil {
		t.Fatalf("apply classification %s: %v", name, err)
	}
	created, err := store.Create(ctx, a)
	if err != nil {
		t.Fatalf("create asset %s: %v", name, err)
	}
	return created
}

// THIS MUST BITE: a freshly-created CI (LastSeen just set by Create) is never
// reported stale, and an old or never-confirmed (NULL) one always is — for
// active/maintenance CIs only. Retired excludes even a NULL LastSeen: it is
// not in service to go stale (soft-retire, mirroring List's own default).
func TestAssetHealth_StaleDetection_RespectsThreshold(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-health-stale")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	fresh := newHealthAsset(t, ctx, store, tn.TenantID, "server", "fresh-host", "production", "high", nil)
	if fresh.LastSeen == nil {
		t.Fatal("Create must set LastSeen")
	}

	old := newHealthAsset(t, ctx, store, tn.TenantID, "server", "old-host", "production", "high", nil)
	oldAt := time.Now().UTC().Add(-40 * 24 * time.Hour)
	backdateLastSeen(t, ctx, scoped, old.AssetID, &oldAt)

	neverConfirmed := newHealthAsset(t, ctx, store, tn.TenantID, "server", "never-confirmed-host", "production", "high", nil)
	backdateLastSeen(t, ctx, scoped, neverConfirmed.AssetID, nil)

	retiredNeverConfirmed := newHealthAsset(t, ctx, store, tn.TenantID, "server", "retired-never-confirmed", "production", "high", nil)
	backdateLastSeen(t, ctx, scoped, retiredNeverConfirmed.AssetID, nil)
	if _, err := store.SetStatus(ctx, retiredNeverConfirmed.AssetID, retiredNeverConfirmed.RowVersion, domain.AssetRetired); err != nil {
		t.Fatalf("retire: %v", err)
	}

	report, err := store.Health(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("health: %v", err)
	}

	stale := map[string]bool{}
	for _, s := range report.Stale.Samples {
		stale[s.AssetID] = true
	}
	if stale[fresh.AssetID] {
		t.Error("a freshly-created CI must never show as stale")
	}
	if !stale[old.AssetID] {
		t.Error("a CI confirmed 40 days ago (threshold 30d) must show as stale")
	}
	if !stale[neverConfirmed.AssetID] {
		t.Error("a CI with NULL last_seen must show as stale")
	}
	if stale[retiredNeverConfirmed.AssetID] {
		t.Error("a retired CI must never show as stale, even with NULL last_seen")
	}
	if report.Stale.Count < 2 {
		t.Errorf("stale count = %d, want at least 2 (old + never-confirmed)", report.Stale.Count)
	}
	if report.StaleAfter != 30*24*time.Hour {
		t.Errorf("StaleAfter = %v, want the requested threshold", report.StaleAfter)
	}

	// Narrowing the threshold below old's actual age excludes it too — the
	// threshold is genuinely respected, not merely "some fixed cutoff".
	tight, err := store.Health(ctx, 41*24*time.Hour)
	if err != nil {
		t.Fatalf("health (tight): %v", err)
	}
	for _, s := range tight.Stale.Samples {
		if s.AssetID == old.AssetID {
			t.Error("a CI confirmed 40 days ago must NOT be stale against a 41-day threshold")
		}
	}
}

// THIS MUST BITE: a CI with a relationship (either direction) is excluded
// from OrphanedAssets; a CI with none is included.
func TestAssetHealth_OrphanDetection(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-health-orphan")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	related1 := newHealthAsset(t, ctx, store, tn.TenantID, "application", "app-1", "production", "high", nil)
	related2 := newHealthAsset(t, ctx, store, tn.TenantID, "database", "db-1", "production", "high", nil)
	rel, err := domain.NewAssetRelationship(tn.TenantID, related1.AssetID, related2.AssetID, domain.RelationshipDependsOn)
	if err != nil {
		t.Fatalf("new relationship: %v", err)
	}
	if _, err := store.CreateRelationship(ctx, rel); err != nil {
		t.Fatalf("create relationship: %v", err)
	}

	orphan := newHealthAsset(t, ctx, store, tn.TenantID, "server", "lonely-host", "production", "high", nil)

	report, err := store.Health(ctx, domain.DefaultAssetStaleAfter)
	if err != nil {
		t.Fatalf("health: %v", err)
	}

	orphaned := map[string]bool{}
	for _, s := range report.OrphanedAssets.Samples {
		orphaned[s.AssetID] = true
	}
	if orphaned[related1.AssetID] {
		t.Error("a CI with an outgoing relationship must never show as orphaned")
	}
	if orphaned[related2.AssetID] {
		t.Error("a CI with an incoming relationship must never show as orphaned")
	}
	if !orphaned[orphan.AssetID] {
		t.Error("a CI with no relationship at all must show as orphaned")
	}
}

// THIS MUST BITE: a business_service with a depends_on/runs_on out-edge is
// excluded from OrphanedBusinessServices; one with none (or only a
// non-composing edge type) is included.
func TestAssetHealth_OrphanedBusinessServiceDetection(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-health-svc-orphan")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	supported := newHealthAsset(t, ctx, store, tn.TenantID, "business_service", "checkout-service", "production", "high", nil)
	ci := newHealthAsset(t, ctx, store, tn.TenantID, "application", "checkout-app", "production", "high", nil)
	rel, err := domain.NewAssetRelationship(tn.TenantID, supported.AssetID, ci.AssetID, domain.RelationshipRunsOn)
	if err != nil {
		t.Fatalf("new relationship: %v", err)
	}
	if _, err := store.CreateRelationship(ctx, rel); err != nil {
		t.Fatalf("create relationship: %v", err)
	}

	unsupported := newHealthAsset(t, ctx, store, tn.TenantID, "business_service", "orphan-service", "production", "high", nil)

	// A network link out of a business_service does not compose it — must
	// still count as orphaned for the service-map's purposes.
	other := newHealthAsset(t, ctx, store, tn.TenantID, "network_device", "switch-1", "production", "high", nil)
	networkRel, err := domain.NewAssetRelationship(tn.TenantID, unsupported.AssetID, other.AssetID, domain.RelationshipConnectedTo)
	if err != nil {
		t.Fatalf("new relationship: %v", err)
	}
	if _, err := store.CreateRelationship(ctx, networkRel); err != nil {
		t.Fatalf("create relationship: %v", err)
	}

	report, err := store.Health(ctx, domain.DefaultAssetStaleAfter)
	if err != nil {
		t.Fatalf("health: %v", err)
	}

	orphanedServices := map[string]bool{}
	for _, s := range report.OrphanedBusinessServices.Samples {
		orphanedServices[s.AssetID] = true
	}
	if orphanedServices[supported.AssetID] {
		t.Error("a business_service with a depends_on/runs_on edge must never show as orphaned")
	}
	if !orphanedServices[unsupported.AssetID] {
		t.Error("a business_service with only a connected_to edge (not composing) must show as orphaned")
	}
	if orphanedServices[ci.AssetID] || orphanedServices[other.AssetID] {
		t.Error("a non-business_service CI must never appear in OrphanedBusinessServices")
	}
}

// THIS MUST BITE: missing owner, unknown criticality and unknown environment
// each independently mark a CI incomplete; a CI with all three set is never
// reported.
func TestAssetHealth_IncompleteDetection(t *testing.T) {
	orgA, _, _, teamA := assetOwnerFixture(t)
	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(&domain.Tenant{TenantID: orgA.TenantID})

	unowned := newHealthAsset(t, ctx, store, orgA.TenantID, "server", "unowned-host", "production", "high", nil)
	unknownCriticality := newHealthAsset(t, ctx, store, orgA.TenantID, "server", "unrated-host", "production", "", &teamA.TeamID)
	unknownEnvironment := newHealthAsset(t, ctx, store, orgA.TenantID, "server", "unplaced-host", "", "high", &teamA.TeamID)
	complete := newHealthAsset(t, ctx, store, orgA.TenantID, "server", "complete-host", "production", "high", &teamA.TeamID)

	report, err := store.Health(ctx, domain.DefaultAssetStaleAfter)
	if err != nil {
		t.Fatalf("health: %v", err)
	}

	incomplete := map[string]bool{}
	for _, s := range report.Incomplete.Samples {
		incomplete[s.AssetID] = true
	}
	if !incomplete[unowned.AssetID] {
		t.Error("an unowned CI must show as incomplete")
	}
	if !incomplete[unknownCriticality.AssetID] {
		t.Error("a CI with unknown criticality must show as incomplete")
	}
	if !incomplete[unknownEnvironment.AssetID] {
		t.Error("a CI with unknown environment must show as incomplete")
	}
	if incomplete[complete.AssetID] {
		t.Error("a fully-classified, owned CI must never show as incomplete")
	}
}

// THIS MUST BITE: GET /admin/assets/health (AssetStore.Health) is
// tenant-scoped by row-level security — tenant B's report never contains a
// trace of tenant A's rotting CMDB, even when both have stale/orphaned/
// incomplete CIs of the identical shape.
func TestAssetHealth_RLSIsolatesByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a := assetTenant(t, tenants, "health-rls-alpha")
	b := assetTenant(t, tenants, "health-rls-bravo")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	// Tenant A: one stale (backdated, NULL-owner) CI, one orphan, done.
	aStale := newHealthAsset(t, ctxA, store, a.TenantID, "server", "alpha-stale", "production", "high", nil)
	backdateLastSeen(t, ctxA, scoped, aStale.AssetID, nil)

	// Tenant B: the identical shape, under its own tenant.
	bStale := newHealthAsset(t, ctxB, store, b.TenantID, "server", "bravo-stale", "production", "high", nil)
	backdateLastSeen(t, ctxB, scoped, bStale.AssetID, nil)

	reportA, err := store.Health(ctxA, domain.DefaultAssetStaleAfter)
	if err != nil {
		t.Fatalf("health A: %v", err)
	}
	reportB, err := store.Health(ctxB, domain.DefaultAssetStaleAfter)
	if err != nil {
		t.Fatalf("health B: %v", err)
	}

	for _, s := range reportA.Stale.Samples {
		if s.AssetID == bStale.AssetID {
			t.Fatal("tenant A's health report contains tenant B's asset")
		}
	}
	for _, s := range reportA.Incomplete.Samples {
		if s.AssetID == bStale.AssetID {
			t.Fatal("tenant A's incomplete report contains tenant B's asset")
		}
	}
	for _, s := range reportB.Stale.Samples {
		if s.AssetID == aStale.AssetID {
			t.Fatal("tenant B's health report contains tenant A's asset")
		}
	}
	staleA, staleB := map[string]bool{}, map[string]bool{}
	for _, s := range reportA.Stale.Samples {
		staleA[s.AssetID] = true
	}
	for _, s := range reportB.Stale.Samples {
		staleB[s.AssetID] = true
	}
	if !staleA[aStale.AssetID] {
		t.Error("tenant A's own stale asset is missing from its own report")
	}
	if !staleB[bStale.AssetID] {
		t.Error("tenant B's own stale asset is missing from its own report")
	}
}

// THIS MUST BITE: Create sets LastSeen, and Update advances it — a manual
// write is itself a confirmation (E1.5).
func TestAssetStore_LastSeen_SetOnCreateAndAdvancedOnUpdate(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-last-seen")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	a, err := domain.NewAsset(tn.TenantID, "server", "host-1", nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	created, err := store.Create(ctx, a)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.LastSeen == nil {
		t.Fatal("Create must set LastSeen")
	}

	// Backdate so a strictly-later Update is unambiguous even at low clock
	// resolution.
	backdatedAt := created.LastSeen.Add(-time.Hour)
	backdateLastSeen(t, ctx, scoped, created.AssetID, &backdatedAt)

	newName := "host-1-renamed"
	updated, err := store.Update(ctx, created.AssetID, created.RowVersion, domain.AssetPatch{Name: &newName})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.LastSeen == nil || !updated.LastSeen.After(backdatedAt) {
		t.Errorf("Update must advance LastSeen past %v, got %v", backdatedAt, updated.LastSeen)
	}
}

// THIS MUST BITE: a no-op re-import still advances LastSeen (a re-import
// re-confirms the CI), but leaves RowVersion, UpdatedAt and history
// untouched — the E1.4 idempotency guarantee is about the CMDB's
// substantive fields, not this derived freshness signal.
func TestAssetStore_Upsert_NoOpReimportAdvancesLastSeenOnly(t *testing.T) {
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-upsert-last-seen")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := assetTestCtx(tn)

	a := newImportedAsset(t, tn.TenantID, "server", "db-primary-01", "aws", "i-touch")
	first, _, err := store.Upsert(ctx, a)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	backdatedAt := first.LastSeen.Add(-time.Hour)
	backdateLastSeen(t, ctx, scoped, first.AssetID, &backdatedAt)

	beforeHist, err := store.History(ctx, first.AssetID, 0, "")
	if err != nil {
		t.Fatalf("history before: %v", err)
	}

	again := newImportedAsset(t, tn.TenantID, "server", "db-primary-01", "aws", "i-touch")
	second, created, err := store.Upsert(ctx, again)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if created {
		t.Fatal("re-importing the same payload must not create a new row")
	}
	if second.RowVersion != first.RowVersion {
		t.Errorf("row_version changed on a no-op re-import: %d -> %d", first.RowVersion, second.RowVersion)
	}
	if second.LastSeen == nil || !second.LastSeen.After(backdatedAt) {
		t.Errorf("a no-op re-import must advance LastSeen past %v, got %v", backdatedAt, second.LastSeen)
	}

	afterHist, err := store.History(ctx, first.AssetID, 0, "")
	if err != nil {
		t.Fatalf("history after: %v", err)
	}
	if len(afterHist) != len(beforeHist) {
		t.Errorf("a no-op re-import's LastSeen touch wrote history: %d -> %d rows", len(beforeHist), len(afterHist))
	}

	// GET /admin/assets/health right after a no-op re-import sweep must not
	// see this CI as stale even against a threshold shorter than the
	// backdated age it started at — the touch is the whole point.
	report, err := store.Health(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	for _, s := range report.Stale.Samples {
		if s.AssetID == first.AssetID {
			t.Error("a CI re-confirmed by a no-op re-import must not show as stale")
		}
	}
}
