//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/graph"
)

// assetTenant creates a real tenant on the privileged pool and returns it,
// mirroring approval_store_integration_test.go's fixture shape.
func assetTenant(t *testing.T, tenants *TenantStore, slug string) *domain.Tenant {
	t.Helper()
	tn, err := tenants.Create(adminTestCtx(), newTenant(slug, "ext-"+slug))
	if err != nil {
		t.Fatalf("create tenant %s: %v", slug, err)
	}
	return tn
}

func TestAssetStore_CreateGetUpdateDelete(t *testing.T) {
	testPool(t) // ensures migrations are applied before the scoped pool is used
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-crud")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := domain.WithTenant(context.Background(), tn)

	a, err := domain.NewAsset(tn.TenantID, "server", "db-primary-01", map[string]any{"region": "us-east-1"})
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	created, err := store.Create(ctx, a)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RowVersion != 1 {
		t.Errorf("row version = %d, want 1", created.RowVersion)
	}
	if created.Attributes["region"] != "us-east-1" {
		t.Errorf("attributes round-trip: %+v", created.Attributes)
	}

	got, err := store.Get(ctx, created.AssetID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "db-primary-01" || got.Type != "server" {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	newName := "db-primary-02"
	newStatus := domain.AssetRetired
	updated, err := store.Update(ctx, created.AssetID, created.RowVersion,
		&newName, map[string]any{"region": "us-west-2"}, &newStatus)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != newName || updated.Status != domain.AssetRetired ||
		updated.Attributes["region"] != "us-west-2" || updated.RowVersion != created.RowVersion+1 {
		t.Errorf("unexpected update result: %+v", updated)
	}

	if err := store.Delete(ctx, created.AssetID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(ctx, created.AssetID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get after delete = %v, want ErrNotFound", err)
	}
}

func TestAssetStore_UpdateDetectsVersionConflict(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-conflict")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := domain.WithTenant(context.Background(), tn)

	a, _ := domain.NewAsset(tn.TenantID, "server", "Host", nil)
	created, err := store.Create(ctx, a)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	renamed := "Host Renamed"
	if _, err := store.Update(ctx, created.AssetID, created.RowVersion, &renamed, nil, nil); err != nil {
		t.Fatalf("first update: %v", err)
	}

	stale := "Stale Rename"
	if _, err := store.Update(ctx, created.AssetID, created.RowVersion, &stale, nil, nil); !errors.Is(err, domain.ErrVersionMismatch) {
		t.Errorf("stale update = %v, want ErrVersionMismatch", err)
	}
}

func TestAssetStore_GetUnknownIsNotFound(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-unknown")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := domain.WithTenant(context.Background(), tn)

	if _, err := store.Get(ctx, "no-such-asset"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get unknown asset = %v, want ErrNotFound", err)
	}
}

// The security-critical property: an asset created under tenant A must be
// invisible — not merely "refused" — to tenant B, both by direct Get and by
// the unfiltered List.
func TestAssetStore_RLSIsolatesAssetsByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("asset-alpha", "ext-asset-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("asset-bravo", "ext-asset-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)

	ctxA := domain.WithTenant(context.Background(), a)
	ctxB := domain.WithTenant(context.Background(), b)

	assetA, _ := domain.NewAsset(a.TenantID, "server", "tenant-a-host", nil)
	createdA, err := store.Create(ctxA, assetA)
	if err != nil {
		t.Fatalf("create for tenant a: %v", err)
	}
	assetB, _ := domain.NewAsset(b.TenantID, "server", "tenant-b-host", nil)
	if _, err := store.Create(ctxB, assetB); err != nil {
		t.Fatalf("create for tenant b: %v", err)
	}

	// Direct Get: tenant B naming tenant A's asset id sees nothing — not a
	// distinguishable "forbidden", the row is simply absent.
	if _, err := store.Get(ctxB, createdA.AssetID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("tenant B reading tenant A's asset = %v, want ErrNotFound", err)
	}

	// List: tenant B's page contains only tenant B's own asset, regardless of
	// how many other tenants' assets exist in the table.
	pageB, err := store.List(ctxB, 0, "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	for _, got := range pageB {
		if got.AssetID == createdA.AssetID {
			t.Fatalf("tenant B's list leaked tenant A's asset: %+v", got)
		}
	}
	foundOwn := false
	for _, got := range pageB {
		if got.Name == "tenant-b-host" {
			foundOwn = true
		}
	}
	if !foundOwn {
		t.Error("tenant B's own asset is missing from tenant B's list")
	}
}

func TestAssetRelationship_CreateListDelete(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-rel-crud")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := domain.WithTenant(context.Background(), tn)

	app, _ := domain.NewAsset(tn.TenantID, "application", "checkout-svc", nil)
	app, err := store.Create(ctx, app)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	db, _ := domain.NewAsset(tn.TenantID, "database", "checkout-db", nil)
	db, err = store.Create(ctx, db)
	if err != nil {
		t.Fatalf("create db: %v", err)
	}

	rel, _ := domain.NewAssetRelationship(tn.TenantID, app.AssetID, db.AssetID, domain.RelationshipDependsOn)
	created, err := store.CreateRelationship(ctx, rel)
	if err != nil {
		t.Fatalf("create relationship: %v", err)
	}
	if created.RowVersion != 1 {
		t.Errorf("row version = %d, want 1", created.RowVersion)
	}

	from, err := store.RelationshipsFrom(ctx, app.AssetID)
	if err != nil {
		t.Fatalf("relationships from: %v", err)
	}
	if len(from) != 1 || from[0].ToAssetID != db.AssetID {
		t.Errorf("unexpected out-edges: %+v", from)
	}

	to, err := store.RelationshipsTo(ctx, db.AssetID)
	if err != nil {
		t.Fatalf("relationships to: %v", err)
	}
	if len(to) != 1 || to[0].FromAssetID != app.AssetID {
		t.Errorf("unexpected in-edges: %+v", to)
	}

	if err := store.DeleteRelationship(ctx, created.RelationshipID); err != nil {
		t.Fatalf("delete relationship: %v", err)
	}
	if err := store.DeleteRelationship(ctx, created.RelationshipID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
}

// Deleting an asset removes the relationships naming it (ON DELETE CASCADE).
func TestAssetStore_DeleteCascadesToRelationships(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-cascade")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := domain.WithTenant(context.Background(), tn)

	app, _ := domain.NewAsset(tn.TenantID, "application", "app", nil)
	app, _ = store.Create(ctx, app)
	db, _ := domain.NewAsset(tn.TenantID, "database", "db", nil)
	db, _ = store.Create(ctx, db)
	rel, _ := domain.NewAssetRelationship(tn.TenantID, app.AssetID, db.AssetID, domain.RelationshipDependsOn)
	if _, err := store.CreateRelationship(ctx, rel); err != nil {
		t.Fatalf("create relationship: %v", err)
	}

	if err := store.Delete(ctx, app.AssetID); err != nil {
		t.Fatalf("delete app: %v", err)
	}

	remaining, err := store.RelationshipsTo(ctx, db.AssetID)
	if err != nil {
		t.Fatalf("relationships to: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("relationship survived its endpoint's deletion: %+v", remaining)
	}
}

// The security-critical property this ADR exists for: a relationship cannot
// be created naming another tenant's asset, even though the foreign key
// alone (REFERENCES asset (asset_id)) would not have stopped it — PostgreSQL
// FK checks bypass row-level security on the referenced table
// (ADR-ASSET-001 §6).
func TestAssetRelationship_CannotCrossTenants(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("asset-rel-alpha", "ext-asset-rel-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("asset-rel-bravo", "ext-asset-rel-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctxA := domain.WithTenant(context.Background(), a)
	ctxB := domain.WithTenant(context.Background(), b)

	victimAsset, _ := domain.NewAsset(a.TenantID, "server", "victim-host", nil)
	victim, err := store.Create(ctxA, victimAsset)
	if err != nil {
		t.Fatalf("create victim asset: %v", err)
	}
	attackerAsset, _ := domain.NewAsset(b.TenantID, "server", "attacker-host", nil)
	attacker, err := store.Create(ctxB, attackerAsset)
	if err != nil {
		t.Fatalf("create attacker asset: %v", err)
	}

	// Tenant B attempts to create a relationship naming tenant A's asset as
	// the target. It must fail exactly as if the id did not exist at all.
	rel, err := domain.NewAssetRelationship(b.TenantID, attacker.AssetID, victim.AssetID, domain.RelationshipDependsOn)
	if err != nil {
		t.Fatalf("new relationship: %v", err)
	}
	if _, err := store.CreateRelationship(ctxB, rel); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-tenant relationship create = %v, want ErrNotFound", err)
	}

	// No row was created — neither tenant sees an edge.
	fromAttacker, err := store.RelationshipsFrom(ctxB, attacker.AssetID)
	if err != nil {
		t.Fatalf("relationships from attacker: %v", err)
	}
	if len(fromAttacker) != 0 {
		t.Fatalf("a cross-tenant relationship was created: %+v", fromAttacker)
	}
	toVictim, err := store.RelationshipsTo(ctxA, victim.AssetID)
	if err != nil {
		t.Fatalf("relationships to victim: %v", err)
	}
	if len(toVictim) != 0 {
		t.Fatalf("the victim's asset gained an edge it never authorised: %+v", toVictim)
	}
}

// The CMDB traversal engine, reused unchanged from the configuration-object
// dependency graph (ADR-ASSET-001 §4): a small DAG's transitive dependencies
// and dependents must be exactly the reachable set, no more and no less.
//
//	checkout-svc --depends_on--> checkout-db --runs_on--> host-1
//	checkout-svc --depends_on--> cache --runs_on--> host-2
func TestAssetGraph_TraversalOverCMDB(t *testing.T) {
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), "asset-graph")

	scoped := tenantScopedPool(t)
	store := NewAssetStore(scoped)
	ctx := domain.WithTenant(context.Background(), tn)

	mk := func(assetType, name string) *domain.Asset {
		a, err := domain.NewAsset(tn.TenantID, assetType, name, nil)
		if err != nil {
			t.Fatalf("new asset %s: %v", name, err)
		}
		created, err := store.Create(ctx, a)
		if err != nil {
			t.Fatalf("create asset %s: %v", name, err)
		}
		return created
	}
	link := func(from, to *domain.Asset, kind domain.RelationshipType) {
		rel, err := domain.NewAssetRelationship(tn.TenantID, from.AssetID, to.AssetID, kind)
		if err != nil {
			t.Fatalf("new relationship: %v", err)
		}
		if _, err := store.CreateRelationship(ctx, rel); err != nil {
			t.Fatalf("create relationship %s->%s: %v", from.Name, to.Name, err)
		}
	}

	app := mk("application", "checkout-svc")
	db := mk("database", "checkout-db")
	cache := mk("service", "cache")
	host1 := mk("server", "host-1")
	host2 := mk("server", "host-2")
	unrelated := mk("server", "unrelated-host")
	_ = unrelated

	link(app, db, domain.RelationshipDependsOn)
	link(app, cache, domain.RelationshipDependsOn)
	link(db, host1, domain.RelationshipRunsOn)
	link(cache, host2, domain.RelationshipRunsOn)

	repo := NewAssetGraphRepo(scoped)
	svc := graph.NewService(repo)

	deps, err := svc.WalkDependencies(ctx, app.AssetID)
	if err != nil {
		t.Fatalf("walk dependencies: %v", err)
	}
	gotDeps := map[string]bool{}
	for _, n := range deps.Nodes {
		gotDeps[n.CfgID] = true
	}
	for _, want := range []*domain.Asset{db, cache, host1, host2} {
		if !gotDeps[want.AssetID] {
			t.Errorf("transitive dependencies of %s missing %s", app.Name, want.Name)
		}
	}
	if gotDeps[unrelated.AssetID] {
		t.Error("transitive dependencies included an unrelated asset")
	}
	if gotDeps[app.AssetID] {
		t.Error("transitive dependencies included the root itself")
	}

	dependents, err := svc.WalkDependents(ctx, host1.AssetID)
	if err != nil {
		t.Fatalf("walk dependents: %v", err)
	}
	gotDependents := map[string]bool{}
	for _, n := range dependents.Nodes {
		gotDependents[n.CfgID] = true
	}
	for _, want := range []*domain.Asset{db, app} {
		if !gotDependents[want.AssetID] {
			t.Errorf("transitive dependents of %s missing %s", host1.Name, want.Name)
		}
	}
	if gotDependents[cache.AssetID] || gotDependents[host2.AssetID] {
		t.Error("transitive dependents of host-1 must not include the cache branch")
	}

	// Direct (one-hop) traversal via the repository itself.
	directDeps, err := repo.Dependencies(ctx, app.AssetID)
	if err != nil {
		t.Fatalf("direct dependencies: %v", err)
	}
	if len(directDeps) != 2 {
		t.Errorf("direct dependencies of %s = %v, want exactly db and cache", app.Name, directDeps)
	}
}
