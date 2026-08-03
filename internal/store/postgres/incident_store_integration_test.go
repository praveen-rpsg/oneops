//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/domain"
)

// incidentTenant creates a real tenant on the privileged pool and returns it,
// mirroring assetTenant's fixture shape.
func incidentTenant(t *testing.T, tenants *TenantStore, slug string) *domain.Tenant {
	t.Helper()
	tn, err := tenants.Create(adminTestCtx(), newTenant(slug, "ext-"+slug))
	if err != nil {
		t.Fatalf("create tenant %s: %v", slug, err)
	}
	return tn
}

// incidentTestCtx carries both the tenant binding IncidentStore's RLS-scoped
// pool requires and the actor identity Create/SetStatus/Assign require to
// attribute an incident_event row — mirrors assetTestCtx.
func incidentTestCtx(tn *domain.Tenant) context.Context {
	return incidentTestCtxAs(tn, "test-incident-actor")
}

func incidentTestCtxAs(tn *domain.Tenant, actor string) context.Context {
	return domain.WithActor(domain.WithTenant(context.Background(), tn), actor)
}

func TestIncidentStore_CreateGetListUpdate(t *testing.T) {
	testPool(t) // ensures migrations are applied before the scoped pool is used
	priv := testPool(t)
	tn := incidentTenant(t, NewTenantStore(priv), "incident-crud")

	scoped := tenantScopedPool(t)
	store := NewIncidentStore(scoped)
	ctx := incidentTestCtx(tn)

	inc, err := domain.NewIncident(tn.TenantID, "db down", "primary unreachable", domain.IncidentSeverityCritical, nil, nil)
	if err != nil {
		t.Fatalf("new incident: %v", err)
	}
	created, err := store.Create(ctx, inc)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.RowVersion != 1 {
		t.Errorf("row_version = %d, want 1", created.RowVersion)
	}
	if created.Status != domain.IncidentOpen {
		t.Errorf("status = %q, want open", created.Status)
	}

	got, err := store.Get(ctx, created.IncidentID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != "db down" {
		t.Errorf("title = %q, want %q", got.Title, "db down")
	}

	page, err := store.List(ctx, 0, "", "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, i := range page {
		if i.IncidentID == created.IncidentID {
			found = true
		}
	}
	if !found {
		t.Error("list did not include the created incident")
	}

	newTitle := "db down (renamed)"
	updated, err := store.Update(ctx, created.IncidentID, created.RowVersion, domain.IncidentPatch{Title: &newTitle})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Title != newTitle {
		t.Errorf("title = %q, want %q", updated.Title, newTitle)
	}
	if updated.RowVersion != created.RowVersion+1 {
		t.Errorf("row_version = %d, want %d", updated.RowVersion, created.RowVersion+1)
	}
}

// THIS MUST BITE: tenant B cannot see tenant A's incidents, either through
// List/Get or through a direct query on the tenant-scoped connection —
// row-level security, not a WHERE clause the store remembered to add.
func TestIncidentStore_RLSIsolatesByTenant(t *testing.T) {
	priv := testPool(t)
	tenants := NewTenantStore(priv)
	a, err := tenants.Create(adminTestCtx(), newTenant("incident-rls-alpha", "ext-incident-rls-alpha"))
	if err != nil {
		t.Fatalf("create tenant a: %v", err)
	}
	b, err := tenants.Create(adminTestCtx(), newTenant("incident-rls-bravo", "ext-incident-rls-bravo"))
	if err != nil {
		t.Fatalf("create tenant b: %v", err)
	}

	scoped := tenantScopedPool(t)
	store := NewIncidentStore(scoped)
	ctxA := incidentTestCtx(a)
	ctxB := incidentTestCtx(b)

	incA, _ := domain.NewIncident(a.TenantID, "tenant a incident", "", domain.IncidentSeverityLow, nil, nil)
	createdA, err := store.Create(ctxA, incA)
	if err != nil {
		t.Fatalf("create for tenant a: %v", err)
	}

	if _, err := store.Get(ctxB, createdA.IncidentID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("tenant B get tenant A's incident = %v, want ErrNotFound", err)
	}

	pageB, err := store.List(ctxB, 0, "", "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	for _, i := range pageB {
		if i.IncidentID == createdA.IncidentID {
			t.Fatal("tenant B's list included tenant A's incident")
		}
	}

	// Direct query on tenant B's own tenant-scoped connection: the property
	// that actually matters is RLS, not a predicate the store happened to add.
	var count int
	if err := scoped.QueryRow(ctxB,
		`SELECT count(*) FROM incident WHERE incident_id = $1`, createdA.IncidentID).Scan(&count); err != nil {
		t.Fatalf("direct query as tenant b: %v", err)
	}
	if count != 0 {
		t.Errorf("tenant B's own connection saw %d of tenant A's incident row(s) directly", count)
	}

	pageA, err := store.List(ctxA, 0, "", "")
	if err != nil {
		t.Fatalf("list as tenant a: %v", err)
	}
	if len(pageA) == 0 {
		t.Error("tenant A cannot see its own incident")
	}
}

// incidentRefFixture builds two tenants — each realised through its own
// organisation, since Team.OrgID is required — one Asset owned by tenant A,
// and one user with an active membership in ONLY tenant A. This is the shape
// the cross-tenant asset_id/assignee_user_id defense tests need: an asset and
// a user that tenant B has no way to know about.
func incidentRefFixture(t *testing.T) (orgA, orgB *domain.Organization, user *domain.User, assetA *domain.Asset) {
	t.Helper()
	priv := testPool(t)
	ctx := adminTestCtx()

	newOrg := func(prefix string) *domain.Organization {
		o, err := domain.NewOrganization(prefix+" Probe",
			strings.ToLower(prefix+"-"+ulid.Make().String()[:10]))
		if err != nil {
			t.Fatalf("new org %s: %v", prefix, err)
		}
		created, err := NewOrganizationStore(priv).Create(ctx, o)
		if err != nil {
			t.Fatalf("create org %s: %v", prefix, err)
		}
		return created
	}
	orgA = newOrg("incident-ref-a")
	orgB = newOrg("incident-ref-b")

	user = newTestUser(t)
	if _, err := NewUserStore(priv).Create(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	m, err := domain.NewMembership(orgA.OrgID, orgA.TenantID, user.UserID)
	if err != nil {
		t.Fatalf("new membership: %v", err)
	}
	if _, err := NewMembershipStore(priv).Grant(ctx, m); err != nil {
		t.Fatalf("grant membership: %v", err)
	}

	assetStore := NewAssetStore(tenantScopedPool(t))
	a, err := domain.NewAsset(orgA.TenantID, "server", "incident-ref-asset", nil)
	if err != nil {
		t.Fatalf("new asset: %v", err)
	}
	assetA, err = assetStore.Create(assetTestCtx(&domain.Tenant{TenantID: orgA.TenantID}), a)
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	return orgA, orgB, user, assetA
}

// THE SECURITY-CRITICAL PROPERTY this test exists to prove BITES: tenant B
// cannot link its incident to tenant A's asset, nor assign it to a user who
// is only a member of tenant A's organisation — even though the foreign keys
// alone would not have stopped either (asset's row-level security is
// bypassed by PostgreSQL's FK check per ADR-ASSET-001 §6, and app_user
// carries no tenant_id at all).
func TestIncidentStore_AssetAndAssigneeRefsCannotCrossTenants(t *testing.T) {
	orgA, orgB, user, assetA := incidentRefFixture(t)
	scoped := tenantScopedPool(t)
	store := NewIncidentStore(scoped)

	ctxA := incidentTestCtx(&domain.Tenant{TenantID: orgA.TenantID})
	ctxB := incidentTestCtx(&domain.Tenant{TenantID: orgB.TenantID})

	// Tenant B attempting to link its incident to tenant A's asset.
	byAsset, err := domain.NewIncident(orgB.TenantID, "tenant b incident (asset)", "", domain.IncidentSeverityLow, &assetA.AssetID, nil)
	if err != nil {
		t.Fatalf("new incident: %v", err)
	}
	if _, err := store.Create(ctxB, byAsset); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-tenant asset_id create = %v, want ErrNotFound", err)
	}

	// Tenant B attempting to assign its incident to a user who is only a
	// member of tenant A's organisation.
	byAssignee, err := domain.NewIncident(orgB.TenantID, "tenant b incident (assignee)", "", domain.IncidentSeverityLow, nil, &user.UserID)
	if err != nil {
		t.Fatalf("new incident: %v", err)
	}
	if _, err := store.Create(ctxB, byAssignee); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-tenant assignee_user_id create = %v, want ErrNotFound", err)
	}

	// No row was created under either attempt.
	pageB, err := store.List(ctxB, 0, "", "")
	if err != nil {
		t.Fatalf("list as tenant b: %v", err)
	}
	if len(pageB) != 0 {
		t.Fatalf("a cross-tenant reference produced a row anyway: %+v", pageB)
	}

	// The same references succeed for tenant A, who can actually see them —
	// proving the refusal above is the tenant boundary, not a broken check.
	ownedA, err := domain.NewIncident(orgA.TenantID, "tenant a incident", "", domain.IncidentSeverityLow, &assetA.AssetID, &user.UserID)
	if err != nil {
		t.Fatalf("new incident: %v", err)
	}
	created, err := store.Create(ctxA, ownedA)
	if err != nil {
		t.Fatalf("same-tenant refs were rejected: %v", err)
	}
	if created.AssetID == nil || *created.AssetID != assetA.AssetID {
		t.Errorf("asset_id not persisted: %+v", created.AssetID)
	}
	if created.AssigneeUserID == nil || *created.AssigneeUserID != user.UserID {
		t.Errorf("assignee_user_id not persisted: %+v", created.AssigneeUserID)
	}

	// The same defense applies to Update/Assign on an incident that genuinely
	// exists, not just to Create.
	unlinked, err := domain.NewIncident(orgB.TenantID, "tenant b unlinked", "", domain.IncidentSeverityLow, nil, nil)
	if err != nil {
		t.Fatalf("new incident: %v", err)
	}
	unlinked, err = store.Create(ctxB, unlinked)
	if err != nil {
		t.Fatalf("create unlinked incident: %v", err)
	}
	if _, err := store.Update(ctxB, unlinked.IncidentID, unlinked.RowVersion,
		domain.IncidentPatch{AssetID: &assetA.AssetID}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-tenant asset_id patch = %v, want ErrNotFound", err)
	}
	if _, err := store.Assign(ctxB, unlinked.IncidentID, unlinked.RowVersion, &user.UserID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cross-tenant assign = %v, want ErrNotFound", err)
	}

	// No partial write: re-reading shows the incident untouched.
	still, err := store.Get(ctxB, unlinked.IncidentID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if still.AssetID != nil || still.AssigneeUserID != nil || still.RowVersion != unlinked.RowVersion {
		t.Errorf("a refused cross-tenant reference mutated the row: %+v", still)
	}
}

// Assign is the single authority for the assignee field: it clears with a
// nil assignee and re-verifies a non-nil one, and it records a timeline row.
func TestIncidentStore_Assign(t *testing.T) {
	priv := testPool(t)
	tn := incidentTenant(t, NewTenantStore(priv), "incident-assign")
	scoped := tenantScopedPool(t)
	store := NewIncidentStore(scoped)
	ctx := incidentTestCtx(tn)

	inc, _ := domain.NewIncident(tn.TenantID, "assign me", "", domain.IncidentSeverityMedium, nil, nil)
	created, err := store.Create(ctx, inc)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.AssigneeUserID != nil {
		t.Fatalf("a freshly created incident must be unassigned: %+v", created.AssigneeUserID)
	}

	// Assigning to a nonexistent/unknown user is refused.
	bogus := "no-such-user"
	if _, err := store.Assign(ctx, created.IncidentID, created.RowVersion, &bogus); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("assign to unknown user = %v, want ErrNotFound", err)
	}

	// Clearing an already-unassigned incident is a legal no-op.
	cleared, err := store.Assign(ctx, created.IncidentID, created.RowVersion, nil)
	if err != nil {
		t.Fatalf("clear (no-op) assign: %v", err)
	}
	if cleared.AssigneeUserID != nil {
		t.Errorf("assignee_user_id = %v, want nil", cleared.AssigneeUserID)
	}
}
