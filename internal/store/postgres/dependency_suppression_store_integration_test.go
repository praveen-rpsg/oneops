//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// depSuppressionFixture is the common CMDB scaffolding every test below
// needs: a real tenant, a tenant-scoped AssetStore/AlertRuleStore, and the
// privileged AlertRuleStore/DependencySuppressionStore Suppress itself runs
// on — mirroring TestAssetGraph_TraversalOverCMDB's own mk/link helper shape.
type depSuppressionFixture struct {
	t      *testing.T
	tn     *domain.Tenant
	ctx    context.Context
	assets *AssetStore
	rules  *AlertRuleStore
	priv   *AlertRuleStore
	store  *DependencySuppressionStore
}

func newDepSuppressionFixture(t *testing.T, slug string) *depSuppressionFixture {
	t.Helper()
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), slug)
	scoped := tenantScopedPool(t)
	graph := NewAssetGraphRepo(scoped)
	return &depSuppressionFixture{
		t:      t,
		tn:     tn,
		ctx:    assetTestCtx(tn),
		assets: NewAssetStore(scoped),
		rules:  NewAlertRuleStore(scoped),
		priv:   NewAlertRuleStore(priv),
		store:  NewDependencySuppressionStore(priv, graph, 0),
	}
}

func (f *depSuppressionFixture) asset(name string) *domain.Asset {
	f.t.Helper()
	a, err := domain.NewAsset(f.tn.TenantID, "server", name, nil)
	if err != nil {
		f.t.Fatalf("new asset %s: %v", name, err)
	}
	created, err := f.assets.Create(f.ctx, a)
	if err != nil {
		f.t.Fatalf("create asset %s: %v", name, err)
	}
	return created
}

func (f *depSuppressionFixture) link(from, to *domain.Asset, kind domain.RelationshipType) {
	f.t.Helper()
	rel, err := domain.NewAssetRelationship(f.tn.TenantID, from.AssetID, to.AssetID, kind)
	if err != nil {
		f.t.Fatalf("new relationship: %v", err)
	}
	if _, err := f.assets.CreateRelationship(f.ctx, rel); err != nil {
		f.t.Fatalf("create relationship %s->%s (%s): %v", from.Name, to.Name, kind, err)
	}
}

// makeDown creates an enabled alert_rule on asset and drives it to firing —
// "down" per ADR-ALERTING-003 §1's definition: >=1 enabled, firing alert_rule
// of its own.
func (f *depSuppressionFixture) makeDown(asset *domain.Asset) {
	f.t.Helper()
	r, err := domain.NewAlertRule(f.tn.TenantID, asset.AssetID, "cpu_utilization", domain.ComparatorGT, 90, 60, domain.AlertSeverityCritical)
	if err != nil {
		f.t.Fatalf("new alert rule: %v", err)
	}
	created, err := f.rules.Create(f.ctx, r)
	if err != nil {
		f.t.Fatalf("create alert rule: %v", err)
	}
	if _, err := f.priv.RecordTransition(context.Background(), created.RuleID, created.RowVersion,
		domain.AlertRuleStateFiring, time.Now().UTC(), nil); err != nil {
		f.t.Fatalf("record transition to firing: %v", err)
	}
}

// TestDependencySuppressionStore_SuppressesWhenADependencyIsDown is the core
// payoff: X depends_on Y, Y is down — Suppress(X) reports suppressed, names Y
// as the root, and the recording is queryable and atomic (suppressed_count
// incremented, last_suppressed_at set, never a silent drop).
func TestDependencySuppressionStore_SuppressesWhenADependencyIsDown(t *testing.T) {
	f := newDepSuppressionFixture(t, "depsup-basic")
	x := f.asset("app-x")
	y := f.asset("db-y")
	f.link(x, y, domain.RelationshipDependsOn)
	f.makeDown(y)

	at := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	root, suppressed, err := f.store.Suppress(context.Background(), f.tn.TenantID, x.AssetID, at)
	if err != nil {
		t.Fatalf("suppress: %v", err)
	}
	if !suppressed {
		t.Fatal("expected suppression: X depends on a down Y")
	}
	if root != y.AssetID {
		t.Errorf("root asset = %q, want %q", root, y.AssetID)
	}

	// The recording is queryable and names the root — never a silent drop.
	var count int64
	var lastAt time.Time
	err = f.priv.pool.QueryRow(context.Background(),
		`SELECT suppressed_count, last_suppressed_at FROM dependency_suppression
		  WHERE tenant_id = $1 AND affected_asset_id = $2 AND root_asset_id = $3`,
		f.tn.TenantID, x.AssetID, y.AssetID).Scan(&count, &lastAt)
	if err != nil {
		t.Fatalf("query recording: %v", err)
	}
	if count != 1 {
		t.Errorf("suppressed_count = %d, want 1", count)
	}
	if !lastAt.Truncate(time.Microsecond).Equal(at.Truncate(time.Microsecond)) {
		t.Errorf("last_suppressed_at = %v, want %v", lastAt, at)
	}

	// A second suppressing tick increments the SAME row rather than inserting
	// a second one.
	if _, _, err := f.store.Suppress(context.Background(), f.tn.TenantID, x.AssetID, at.Add(time.Minute)); err != nil {
		t.Fatalf("second suppress: %v", err)
	}
	if err := f.priv.pool.QueryRow(context.Background(),
		`SELECT suppressed_count FROM dependency_suppression
		  WHERE tenant_id = $1 AND affected_asset_id = $2 AND root_asset_id = $3`,
		f.tn.TenantID, x.AssetID, y.AssetID).Scan(&count); err != nil {
		t.Fatalf("re-query recording: %v", err)
	}
	if count != 2 {
		t.Errorf("suppressed_count after a second tick = %d, want 2 (upsert, not a second row)", count)
	}
}

// TestDependencySuppressionStore_NoSuppressionWhenNoDependencyIsDown is the
// non-vacuous counterpart: X depends on Y, but Y is not firing — Suppress
// must not report a suppression, and nothing is recorded.
func TestDependencySuppressionStore_NoSuppressionWhenNoDependencyIsDown(t *testing.T) {
	f := newDepSuppressionFixture(t, "depsup-nodown")
	x := f.asset("app-x")
	y := f.asset("db-y")
	f.link(x, y, domain.RelationshipDependsOn)
	// y has no alert_rule at all: not down.

	_, suppressed, err := f.store.Suppress(context.Background(), f.tn.TenantID, x.AssetID, time.Now().UTC())
	if err != nil {
		t.Fatalf("suppress: %v", err)
	}
	if suppressed {
		t.Fatal("expected no suppression: no dependency is down")
	}

	var n int
	if err := f.priv.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM dependency_suppression WHERE tenant_id = $1 AND affected_asset_id = $2`,
		f.tn.TenantID, x.AssetID).Scan(&n); err != nil {
		t.Fatalf("query recording: %v", err)
	}
	if n != 0 {
		t.Errorf("recorded %d suppressions with nothing down, want 0", n)
	}
}

// TestDependencySuppressionStore_DirectionCorrectness is the serious-bug
// guard the story calls out by name: X does NOT depend on Y — instead Y
// depends on X (Y → X). Y being down must NEVER suppress X: X is the thing
// Y needs, not the other way around. A reversed traversal would wrongly
// suppress X here.
func TestDependencySuppressionStore_DirectionCorrectness(t *testing.T) {
	f := newDepSuppressionFixture(t, "depsup-direction")
	x := f.asset("app-x")
	y := f.asset("app-y-dependent-on-x")
	// Y depends on X — the REVERSE of what would suppress X.
	f.link(y, x, domain.RelationshipDependsOn)
	f.makeDown(y)

	_, suppressed, err := f.store.Suppress(context.Background(), f.tn.TenantID, x.AssetID, time.Now().UTC())
	if err != nil {
		t.Fatalf("suppress: %v", err)
	}
	if suppressed {
		t.Fatal("X was suppressed by a DEPENDENT (Y depends on X) being down — the traversal direction is reversed")
	}

	// The correct direction still works from Y's own side: Y really does
	// depend on X, so if X itself were down, Y (not X) would be suppressed.
	f.makeDown(x)
	_, ySuppressed, err := f.store.Suppress(context.Background(), f.tn.TenantID, y.AssetID, time.Now().UTC())
	if err != nil {
		t.Fatalf("suppress y: %v", err)
	}
	if !ySuppressed {
		t.Fatal("Y (which really does depend on X) was not suppressed when X went down — traversal direction is broken")
	}
}

// TestDependencySuppressionStore_ConnectedToAndMemberOfAreExcluded proves
// ADR-ALERTING-003 §2's edge-type filter: a down peer reachable only via
// connected_to or member_of (not depends_on/runs_on) must not suppress.
func TestDependencySuppressionStore_ConnectedToAndMemberOfAreExcluded(t *testing.T) {
	f := newDepSuppressionFixture(t, "depsup-edgetype")
	x := f.asset("app-x")
	peer := f.asset("connected-peer")
	group := f.asset("shared-group")
	f.link(x, peer, domain.RelationshipConnectedTo)
	f.link(x, group, domain.RelationshipMemberOf)
	f.makeDown(peer)
	f.makeDown(group)

	_, suppressed, err := f.store.Suppress(context.Background(), f.tn.TenantID, x.AssetID, time.Now().UTC())
	if err != nil {
		t.Fatalf("suppress: %v", err)
	}
	if suppressed {
		t.Fatal("X was suppressed by a connected_to/member_of peer being down — those edge types must be excluded")
	}
}

// TestDependencySuppressionStore_CycleSafety proves a depends_on cycle
// (X<->Y) does not hang or over-suppress: Suppress(X) still terminates and
// correctly finds Y down.
func TestDependencySuppressionStore_CycleSafety(t *testing.T) {
	f := newDepSuppressionFixture(t, "depsup-cycle")
	x := f.asset("app-x")
	y := f.asset("app-y")
	f.link(x, y, domain.RelationshipDependsOn)
	f.link(y, x, domain.RelationshipDependsOn) // closes the cycle
	f.makeDown(y)

	done := make(chan struct{})
	var root string
	var suppressed bool
	var err error
	go func() {
		root, suppressed, err = f.store.Suppress(context.Background(), f.tn.TenantID, x.AssetID, time.Now().UTC())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Suppress did not return within 10s — a depends_on cycle hung the traversal")
	}
	if err != nil {
		t.Fatalf("suppress: %v", err)
	}
	if !suppressed || root != y.AssetID {
		t.Errorf("suppressed=%v root=%q, want suppressed=true root=%q", suppressed, root, y.AssetID)
	}
}

// TestDependencySuppressionStore_DownCheckIsTenantIsolated is the
// privileged-pool isolation proof ADR-TENANCY-012 requires, the identical
// adversarial shape TestMaintenanceWindowStore_SuppressIsTenantIsolated
// already uses: two tenants share the exact same asset_id (via a raw insert
// exploiting the same FK-bypasses-RLS crack ADR-ASSET-001 §6 documents), one
// tenant's copy is genuinely down, and only ITS own dependent may be
// suppressed by it.
//
// Mutation-verified: removing "tenant_id = $1" from
// DependencySuppressionStore.Suppress's down-check SQL makes this test fail
// — tenant B's asset would read as down for tenant A's dependent purely
// because they share an asset_id string.
func TestDependencySuppressionStore_DownCheckIsTenantIsolated(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("depsup-tiso-alpha", "ext-depsup-tiso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("depsup-tiso-bravo", "ext-depsup-tiso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assetsA := NewAssetStore(scoped)
	rulesA := NewAlertRuleStore(scoped)
	ctxA := assetTestCtx(a)
	ctxB := assetTestCtx(b)

	// Tenant A: a REAL down asset "shared-y" and a dependent "x-a".
	yA := telemetryAsset(t, assetsA, ctxA, a.TenantID, "shared-y")
	xA, err := domain.NewAsset(a.TenantID, "server", "x-a", nil)
	if err != nil {
		t.Fatalf("new asset x-a: %v", err)
	}
	xA, err = assetsA.Create(ctxA, xA)
	if err != nil {
		t.Fatalf("create x-a: %v", err)
	}
	relA, err := domain.NewAssetRelationship(a.TenantID, xA.AssetID, yA.AssetID, domain.RelationshipDependsOn)
	if err != nil {
		t.Fatalf("new relationship a: %v", err)
	}
	if _, err := assetsA.CreateRelationship(ctxA, relA); err != nil {
		t.Fatalf("create relationship a: %v", err)
	}
	ruleA, err := domain.NewAlertRule(a.TenantID, yA.AssetID, "cpu_utilization", domain.ComparatorGT, 90, 60, domain.AlertSeverityCritical)
	if err != nil {
		t.Fatalf("new alert rule a: %v", err)
	}
	createdRuleA, err := rulesA.Create(ctxA, ruleA)
	if err != nil {
		t.Fatalf("create alert rule a: %v", err)
	}
	privRules := NewAlertRuleStore(priv)
	if _, err := privRules.RecordTransition(context.Background(), createdRuleA.RuleID, createdRuleA.RowVersion,
		domain.AlertRuleStateFiring, time.Now().UTC(), nil); err != nil {
		t.Fatalf("record transition a: %v", err)
	}

	// Tenant B: its OWN asset "x-b" that (via the FK-bypasses-RLS route) is
	// wired to depend on the SAME asset_id string as tenant A's down "shared-y"
	// — but tenant B never declared its own alert_rule against that asset_id.
	xB := telemetryAsset(t, NewAssetStore(scoped), ctxB, b.TenantID, "x-b")
	relB, err := domain.NewAssetRelationship(b.TenantID, xB.AssetID, yA.AssetID, domain.RelationshipDependsOn)
	if err != nil {
		t.Fatalf("new relationship b: %v", err)
	}
	if _, err := priv.Exec(context.Background(),
		`INSERT INTO asset_relationship (relationship_id, tenant_id, from_asset_id, to_asset_id, type, created_at)
		 VALUES ($1, $2, $3, $4, $5, now())`,
		relB.RelationshipID, b.TenantID, xB.AssetID, yA.AssetID, string(domain.RelationshipDependsOn)); err != nil {
		t.Fatalf("raw-insert tenant b's relationship onto tenant a's asset_id: %v", err)
	}

	graph := NewAssetGraphRepo(scoped)
	store := NewDependencySuppressionStore(priv, graph, 0)

	_, suppressedB, err := store.Suppress(context.Background(), b.TenantID, xB.AssetID, time.Now().UTC())
	if err != nil {
		t.Fatalf("suppress tenant b: %v", err)
	}
	if suppressedB {
		t.Error("tenant B was suppressed by tenant A's down asset over a shared asset_id — cross-tenant leak")
	}

	_, suppressedA, err := store.Suppress(context.Background(), a.TenantID, xA.AssetID, time.Now().UTC())
	if err != nil {
		t.Fatalf("suppress tenant a: %v", err)
	}
	if !suppressedA {
		t.Error("tenant a's own down dependency did not suppress its own dependent")
	}
}
