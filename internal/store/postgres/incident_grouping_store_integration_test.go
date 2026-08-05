//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/grouping"
)

// groupingFixture is the common scaffolding E4.2's store tests need: a real
// tenant, a tenant-scoped AssetStore/IncidentStore for setting up CIs and
// incidents through the normal write paths, and the privileged
// IncidentGroupingStore itself — mirroring depSuppressionFixture's shape
// (dependency_suppression_store_integration_test.go).
type groupingFixture struct {
	t        *testing.T
	tn       *domain.Tenant
	ctx      context.Context
	assets   *AssetStore
	priv     *IncidentStore // privileged pool: FindOrCreateOpenAlertIncident, same as the evaluator uses
	scopedIR *IncidentStore // tenant-scoped pool: the admin CRUD surface (SetStatus etc)
	store    *IncidentGroupingStore
}

func newGroupingFixture(t *testing.T, slug string) *groupingFixture {
	t.Helper()
	testPool(t)
	priv := testPool(t)
	tn := assetTenant(t, NewTenantStore(priv), slug)
	scoped := tenantScopedPool(t)
	return &groupingFixture{
		t:        t,
		tn:       tn,
		ctx:      assetTestCtx(tn),
		assets:   NewAssetStore(scoped),
		priv:     NewIncidentStore(priv),
		scopedIR: NewIncidentStore(scoped),
		store:    NewIncidentGroupingStore(priv),
	}
}

func (f *groupingFixture) asset(name string) *domain.Asset {
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

// openAlertIncident creates a real OPEN, alert-sourced incident on asset via
// the same FindOrCreateOpenAlertIncident path the evaluator uses (E4.1),
// returning its id.
func (f *groupingFixture) openAlertIncident(asset *domain.Asset) string {
	f.t.Helper()
	want, err := domain.NewAlertIncident(f.tn.TenantID, "cpu high", "cpu_utilization breached threshold",
		domain.IncidentSeverityCritical, asset.AssetID)
	if err != nil {
		f.t.Fatalf("new alert incident: %v", err)
	}
	id, err := f.priv.FindOrCreateOpenAlertIncident(context.Background(), want, "test-actor", "alert fired")
	if err != nil {
		f.t.Fatalf("find-or-create alert incident: %v", err)
	}
	return id
}

// manualIncident creates a manual (non-alert) incident on asset via the
// tenant-scoped admin CRUD path, so grouping's openAlertIncidentPredicate can
// be proven to exclude it.
func (f *groupingFixture) manualIncident(asset *domain.Asset) string {
	f.t.Helper()
	inc, err := domain.NewIncident(f.tn.TenantID, "manual investigation", "", domain.IncidentSeverityLow, &asset.AssetID, nil)
	if err != nil {
		f.t.Fatalf("new incident: %v", err)
	}
	created, err := f.scopedIR.Create(f.ctx, inc)
	if err != nil {
		f.t.Fatalf("create manual incident: %v", err)
	}
	return created.IncidentID
}

func strp(s string) *string { return &s }

func containsIncident(list []grouping.OpenAlertIncident, id string) bool {
	for _, inc := range list {
		if inc.IncidentID == id {
			return true
		}
	}
	return false
}

func findIncident(list []grouping.OpenAlertIncident, id string) (grouping.OpenAlertIncident, bool) {
	for _, inc := range list {
		if inc.IncidentID == id {
			return inc, true
		}
	}
	return grouping.OpenAlertIncident{}, false
}

// TestIncidentGroupingStore_RoundTrip is the store round-trip: an open
// alert-sourced incident is discoverable, SetRootIncidentID persists and is
// re-read correctly, clearing it back to nil round-trips too, and the write
// touches ONLY root_incident_id (+ row_version/updated_at) — never status —
// proving grouping cannot mutate incident state at the schema level, not just
// by the Go interface's shape (which internal/grouping's own unit tests
// already prove).
func TestIncidentGroupingStore_RoundTrip(t *testing.T) {
	f := newGroupingFixture(t, "grouping-roundtrip")
	x := f.asset("app-x")
	db := f.asset("db-y")
	incX := f.openAlertIncident(x)
	incDB := f.openAlertIncident(db)

	tenants, err := f.store.TenantsWithOpenAlertIncidents(context.Background())
	if err != nil {
		t.Fatalf("list tenants: %v", err)
	}
	found := false
	for _, tid := range tenants {
		if tid == f.tn.TenantID {
			found = true
		}
	}
	if !found {
		t.Fatalf("tenant %s not found among tenants with open alert incidents: %v", f.tn.TenantID, tenants)
	}

	open, err := f.store.OpenAlertIncidents(context.Background(), f.tn.TenantID)
	if err != nil {
		t.Fatalf("open alert incidents: %v", err)
	}
	incXRow, ok := findIncident(open, incX)
	if !ok {
		t.Fatalf("inc-x %s not found in open alert incidents", incX)
	}
	if incXRow.AssetID != x.AssetID {
		t.Errorf("inc-x asset_id = %q, want %q", incXRow.AssetID, x.AssetID)
	}
	if incXRow.RootIncidentID != nil {
		t.Errorf("inc-x root_incident_id = %v, want nil before any grouping write", *incXRow.RootIncidentID)
	}

	// A row_version snapshot before the grouping write, to prove SetRootIncidentID
	// bumps it — a grouping decision is a visible row change, not a side-channel.
	var statusBefore string
	var rowVersionBefore int64
	if err := f.priv.pool.QueryRow(context.Background(),
		`SELECT status, row_version FROM incident WHERE incident_id = $1`, incX).Scan(&statusBefore, &rowVersionBefore); err != nil {
		t.Fatalf("query incident before: %v", err)
	}

	if err := f.store.SetRootIncidentID(context.Background(), f.tn.TenantID, incX, strp(incDB)); err != nil {
		t.Fatalf("set root: %v", err)
	}

	open2, err := f.store.OpenAlertIncidents(context.Background(), f.tn.TenantID)
	if err != nil {
		t.Fatalf("open alert incidents after set: %v", err)
	}
	incXRow2, ok := findIncident(open2, incX)
	if !ok {
		t.Fatalf("inc-x not found after set")
	}
	if incXRow2.RootIncidentID == nil || *incXRow2.RootIncidentID != incDB {
		t.Fatalf("inc-x root_incident_id after set = %v, want %v", incXRow2.RootIncidentID, incDB)
	}

	var statusAfter string
	var rowVersionAfter int64
	if err := f.priv.pool.QueryRow(context.Background(),
		`SELECT status, row_version FROM incident WHERE incident_id = $1`, incX).Scan(&statusAfter, &rowVersionAfter); err != nil {
		t.Fatalf("query incident after: %v", err)
	}
	if statusAfter != statusBefore {
		t.Errorf("status changed from %q to %q — grouping must never mutate incident state", statusBefore, statusAfter)
	}
	if rowVersionAfter != rowVersionBefore+1 {
		t.Errorf("row_version = %d, want %d (exactly one bump for the grouping write)", rowVersionAfter, rowVersionBefore+1)
	}

	// Clearing round-trips too.
	if err := f.store.SetRootIncidentID(context.Background(), f.tn.TenantID, incX, nil); err != nil {
		t.Fatalf("clear root: %v", err)
	}
	open3, err := f.store.OpenAlertIncidents(context.Background(), f.tn.TenantID)
	if err != nil {
		t.Fatalf("open alert incidents after clear: %v", err)
	}
	incXRow3, ok := findIncident(open3, incX)
	if !ok {
		t.Fatalf("inc-x not found after clear")
	}
	if incXRow3.RootIncidentID != nil {
		t.Errorf("inc-x root_incident_id after clear = %v, want nil", *incXRow3.RootIncidentID)
	}
}

// TestIncidentGroupingStore_ExcludesResolvedAndClosedIncidents proves the
// self-healing precondition: an incident that transitions to resolved must
// stop appearing in OpenAlertIncidents (and, once it is the tenant's only
// incident, in TenantsWithOpenAlertIncidents) on the very next read — the same
// definition of "open" FindOrCreateOpenAlertIncident's own uniqueness index
// uses.
func TestIncidentGroupingStore_ExcludesResolvedAndClosedIncidents(t *testing.T) {
	f := newGroupingFixture(t, "grouping-excl-resolved")
	x := f.asset("app-x")
	incX := f.openAlertIncident(x)

	open, err := f.store.OpenAlertIncidents(context.Background(), f.tn.TenantID)
	if err != nil {
		t.Fatalf("open alert incidents: %v", err)
	}
	if !containsIncident(open, incX) {
		t.Fatalf("setup: inc-x not open")
	}

	// Walk it through to resolved via the tenant-scoped admin path.
	current, err := f.scopedIR.Get(f.ctx, incX)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	for _, next := range []domain.IncidentStatus{domain.IncidentAcknowledged, domain.IncidentInvestigating, domain.IncidentResolved} {
		current, err = f.scopedIR.SetStatus(f.ctx, incX, current.RowVersion, next)
		if err != nil {
			t.Fatalf("transition to %s: %v", next, err)
		}
	}

	open2, err := f.store.OpenAlertIncidents(context.Background(), f.tn.TenantID)
	if err != nil {
		t.Fatalf("open alert incidents after resolve: %v", err)
	}
	if containsIncident(open2, incX) {
		t.Errorf("resolved incident %s still reported as open", incX)
	}

	tenants, err := f.store.TenantsWithOpenAlertIncidents(context.Background())
	if err != nil {
		t.Fatalf("list tenants: %v", err)
	}
	for _, tid := range tenants {
		if tid == f.tn.TenantID {
			t.Errorf("tenant %s still listed with open alert incidents after its only incident resolved", f.tn.TenantID)
		}
	}
}

// TestIncidentGroupingStore_ExcludesManualIncidents proves the source filter:
// a manually-filed incident (Source=manual) is never a grouping candidate —
// only IncidentSourceAlert incidents participate, mirroring E4.1's own
// correlation gate (Source is what stops auto-correlation from annexing an
// operator's own incident; here it is what stops grouping from moving one).
func TestIncidentGroupingStore_ExcludesManualIncidents(t *testing.T) {
	f := newGroupingFixture(t, "grouping-excl-manual")
	x := f.asset("app-x")
	manual := f.manualIncident(x)

	open, err := f.store.OpenAlertIncidents(context.Background(), f.tn.TenantID)
	if err != nil {
		t.Fatalf("open alert incidents: %v", err)
	}
	if containsIncident(open, manual) {
		t.Errorf("manual incident %s reported as an open alert incident", manual)
	}
}

// TestIncidentGroupingStore_SetRootIncidentIDIsTenantIsolated is the
// privileged-pool isolation proof ADR-TENANCY-012 requires: naming another
// tenant's incident_id under the WRONG tenant_id must fail closed, never
// silently write. This directly protects against a forged or buggy caller
// handing the reconciler one tenant's incident id while reconciling another.
//
// Mutation-verified: removing "tenant_id = $2" from SetRootIncidentID's UPDATE
// (leaving only "incident_id = $1") makes this test fail — the update would
// match the row regardless of which tenant_id was supplied.
func TestIncidentGroupingStore_SetRootIncidentIDIsTenantIsolated(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("grouping-tiso-alpha", "ext-grouping-tiso-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("grouping-tiso-bravo", "ext-grouping-tiso-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	assetsA := NewAssetStore(scoped)
	privIR := NewIncidentStore(priv)
	store := NewIncidentGroupingStore(priv)

	ctxA := assetTestCtx(a)
	xA, err := domain.NewAsset(a.TenantID, "server", "x-a", nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	xA, err = assetsA.Create(ctxA, xA)
	if err != nil {
		t.Fatalf("create x-a: %v", err)
	}
	wantA, err := domain.NewAlertIncident(a.TenantID, "cpu high", "", domain.IncidentSeverityCritical, xA.AssetID)
	if err != nil {
		t.Fatalf("new alert incident a: %v", err)
	}
	incA, err := privIR.FindOrCreateOpenAlertIncident(context.Background(), wantA, "test-actor", "note")
	if err != nil {
		t.Fatalf("create incident a: %v", err)
	}

	// Tenant B tries to root tenant A's incident under something, quoting its
	// OWN tenant_id — this must not touch tenant A's row at all.
	err = store.SetRootIncidentID(context.Background(), b.TenantID, incA, nil)
	if err == nil {
		t.Fatal("tenant B was able to write tenant A's incident by naming its own tenant_id — cross-tenant write succeeded")
	}

	// Tenant A's own write, quoting its own tenant_id, must still work.
	if err := store.SetRootIncidentID(context.Background(), a.TenantID, incA, nil); err != nil {
		t.Fatalf("tenant a's own write failed: %v", err)
	}
}

// TestIncidentGroupingStore_OpenAlertIncidentsIsTenantIsolated is the
// read-side analogue: two tenants each have their own open alert incident on
// an asset with the exact same name/AssetID-independent identity (asset_id is
// a platform ULID and therefore already globally unique — the risk this
// proves closed is a query that forgets the tenant_id predicate entirely and
// returns every tenant's rows, not merely a shared-string collision).
//
// Mutation-verified: removing "tenant_id = $1" from OpenAlertIncidents's
// SELECT makes this test fail — tenant A's call would also return tenant B's
// incident.
func TestIncidentGroupingStore_OpenAlertIncidentsIsTenantIsolated(t *testing.T) {
	fa := newGroupingFixture(t, "grouping-read-tiso-a")
	fb := newGroupingFixture(t, "grouping-read-tiso-b")

	xa := fa.asset("app-x")
	xb := fb.asset("app-x") // same name, different tenant, different asset_id (ULID)
	incA := fa.openAlertIncident(xa)
	incB := fb.openAlertIncident(xb)

	openA, err := fa.store.OpenAlertIncidents(context.Background(), fa.tn.TenantID)
	if err != nil {
		t.Fatalf("open alert incidents a: %v", err)
	}
	if !containsIncident(openA, incA) {
		t.Fatalf("tenant a's own incident missing from its own read")
	}
	if containsIncident(openA, incB) {
		t.Fatalf("tenant a's read returned tenant b's incident %s — cross-tenant leak", incB)
	}

	openB, err := fb.store.OpenAlertIncidents(context.Background(), fb.tn.TenantID)
	if err != nil {
		t.Fatalf("open alert incidents b: %v", err)
	}
	if containsIncident(openB, incA) {
		t.Fatalf("tenant b's read returned tenant a's incident %s — cross-tenant leak", incA)
	}
}

// TestIncidentGroupingStore_SetRootIncidentIDRejectsUnknownRoot proves the FK
// is real and the store surfaces it rather than silently succeeding: naming a
// root_incident_id that names no row at all must fail.
func TestIncidentGroupingStore_SetRootIncidentIDRejectsUnknownRoot(t *testing.T) {
	f := newGroupingFixture(t, "grouping-unknown-root")
	x := f.asset("app-x")
	incX := f.openAlertIncident(x)

	err := f.store.SetRootIncidentID(context.Background(), f.tn.TenantID, incX, strp("inc_does_not_exist"))
	if err == nil {
		t.Fatal("expected an error naming a non-existent root_incident_id")
	}
}
