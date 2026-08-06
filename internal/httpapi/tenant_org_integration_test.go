//go:build integration

package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/store/migrate"
	"github.com/rpsg/oneops/internal/store/postgres"
)

// tenantOrgHarness wires GET /admin/tenant-org against the REAL
// OrganizationStore — an appPool (NewTenantScopedPool), exactly the pool
// production's composition root builds it over (cmd/controlplane/main.go)
// — plus a privileged pool used ONLY to register tenants and insert
// organization fixtures, never for anything the endpoint itself reads. This
// is the wiring the handler unit tests (fakes, handlers_tenant_org_test.go)
// cannot prove: that the REAL store, queried with the tenant this harness
// resolves from a signed JWT, returns exactly that tenant's organization and
// nothing else.
type tenantOrgHarness struct {
	router  http.Handler
	dsn     string
	priv    *pgxpool.Pool
	appPool *pgxpool.Pool
	tenants *postgres.TenantStore
}

func realTenantOrgHarness(t *testing.T) *tenantOrgHarness {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	dsn := base + sep + "options=-c%20search_path%3Dhttpapi_itest"
	ctx := context.Background()

	priv, err := postgres.NewPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("privileged pool: %v", err)
	}
	var pingErr error
	for i := 0; i < 60; i++ {
		if pingErr = priv.Ping(ctx); pingErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if pingErr != nil {
		t.Fatalf("db not ready: %v", pingErr)
	}
	if _, err := priv.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS httpapi_itest`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := migrate.Up(ctx, priv); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(priv.Close)

	appPool, err := postgres.NewTenantScopedPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("tenant-scoped pool: %v", err)
	}
	t.Cleanup(appPool.Close)

	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), priv.Ping)

	h := &tenantOrgHarness{
		dsn: dsn, priv: priv, appPool: appPool,
		tenants: postgres.NewTenantStore(priv),
	}
	s.SetTenants(h.tenants)
	// The same wiring cmd/controlplane/main.go uses: OrganizationStore over
	// the tenant-scoped appPool.
	s.SetOrganizations(postgres.NewOrganizationStore(appPool))
	h.router = s.Router()
	return h
}

// tenantOrgTenant registers a real tenant on the privileged pool.
func (h *tenantOrgHarness) tenantOrgTenant(t *testing.T, slug string) *domain.Tenant {
	t.Helper()
	slug = uniqueSlug(slug)
	tn, err := h.tenants.Create(domain.WithActor(context.Background(), "test-actor"), &domain.Tenant{
		TenantID: domain.NewID(), Slug: slug, Name: slug, ExternalID: "ext-" + slug,
		Status: domain.TenantActive,
	})
	if err != nil {
		t.Fatalf("create tenant %s: %v", slug, err)
	}
	return tn
}

// createOrgFor inserts a real organization row naming an already-registered
// tenant, directly via the privileged pool rather than
// OrganizationStore.Create: that constructor mints its OWN tenant row
// (domain.NewOrganization always generates a fresh TenantID), which would
// collide with the tenant this harness already registered with a resolvable
// ExternalID for JWT minting. The endpoint's only requirement is a real
// organization row whose tenant_id matches — the same minimal direct-SQL
// fixture tenant_users_integration_test.go's createOrgFor already uses.
func (h *tenantOrgHarness) createOrgFor(t *testing.T, tn *domain.Tenant, slug string) *domain.Organization {
	t.Helper()
	orgID := domain.NewID()
	slug = uniqueSlug(slug)
	if _, err := h.priv.Exec(context.Background(), `
		INSERT INTO organization (org_id, tenant_id, slug, name, status)
		VALUES ($1, $2, $3, $4, 'active')`,
		orgID, tn.TenantID, slug, "Org "+slug); err != nil {
		t.Fatalf("create organization for tenant %s: %v", tn.TenantID, err)
	}
	return &domain.Organization{OrgID: orgID, TenantID: tn.TenantID, Slug: slug, Name: "Org " + slug}
}

// get issues an authenticated GET /v1/admin/tenant-org as tn's admin.
func (h *tenantOrgHarness) get(t *testing.T, tn *domain.Tenant) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-org", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, tn.ExternalID, []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// getAsRole issues an authenticated GET /v1/admin/tenant-org as tn, holding
// the given role(s) instead of oneops-admin.
func (h *tenantOrgHarness) getAsRole(t *testing.T, tn *domain.Tenant, roles []string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-org", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, tn.ExternalID, roles))
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// TestTenantOrgAPI_ReturnsCallersOwnOrg is the correctness proof: a tenant
// administrator's GET /admin/tenant-org resolves to the real organization row
// realising THEIR OWN tenant — org_id, tenant_id and name all traced to the
// fixture, not merely "some JSON came back".
func TestTenantOrgAPI_ReturnsCallersOwnOrg(t *testing.T) {
	h := realTenantOrgHarness(t)
	tn := h.tenantOrgTenant(t, "tenant-org-correctness")
	org := h.createOrgFor(t, tn, "tenant-org-correctness")

	rec := h.get(t, tn)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := decodeOrg(t, rec)
	if got.OrgID != org.OrgID {
		t.Errorf("org_id = %q, want %q", got.OrgID, org.OrgID)
	}
	if got.TenantID != tn.TenantID {
		t.Errorf("tenant_id = %q, want %q", got.TenantID, tn.TenantID)
	}
	if got.Slug != org.Slug {
		t.Errorf("slug = %q, want %q", got.Slug, org.Slug)
	}
	if got.Status != "active" {
		t.Errorf("status = %q, want active", got.Status)
	}
}

// TestTenantOrgAPI_TenantIsolation is the security-critical property this
// story exists to prove: two tenants, each realising their own organization.
// Tenant A's call must yield EXACTLY A's organization; tenant B's call must
// yield EXACTLY B's. The request carries no org_id/tenant_id of any kind —
// the ONLY way this test could pass with the wrong organization coming back
// is if the JWT's resolved tenant genuinely selects the row, which is what
// getTenantOrganization's own tenant-from-context sourcing (not a request
// parameter) makes true. If the handler were ever changed to accept an
// org_id/tenant_id from the request instead of domain.TenantFrom, this test
// would still issue no such parameter and would immediately start failing
// (or trivially "passing" by both calls returning the same wrong row) rather
// than silently validating a broken handler.
func TestTenantOrgAPI_TenantIsolation(t *testing.T) {
	h := realTenantOrgHarness(t)
	tnA := h.tenantOrgTenant(t, "tenant-org-iso-a")
	tnB := h.tenantOrgTenant(t, "tenant-org-iso-b")
	orgA := h.createOrgFor(t, tnA, "tenant-org-iso-a")
	orgB := h.createOrgFor(t, tnB, "tenant-org-iso-b")

	recA := h.get(t, tnA)
	if recA.Code != http.StatusOK {
		t.Fatalf("tenant A: status = %d, want 200: %s", recA.Code, recA.Body.String())
	}
	gotA := decodeOrg(t, recA)

	recB := h.get(t, tnB)
	if recB.Code != http.StatusOK {
		t.Fatalf("tenant B: status = %d, want 200: %s", recB.Code, recB.Body.String())
	}
	gotB := decodeOrg(t, recB)

	if gotA.OrgID != orgA.OrgID {
		t.Errorf("tenant A got org_id = %q, want its own %q", gotA.OrgID, orgA.OrgID)
	}
	if gotA.OrgID == orgB.OrgID {
		t.Errorf("tenant A's call returned tenant B's organization (%q) — cross-tenant leak", orgB.OrgID)
	}
	if gotA.TenantID != tnA.TenantID {
		t.Errorf("tenant A got tenant_id = %q, want its own %q", gotA.TenantID, tnA.TenantID)
	}

	if gotB.OrgID != orgB.OrgID {
		t.Errorf("tenant B got org_id = %q, want its own %q", gotB.OrgID, orgB.OrgID)
	}
	if gotB.OrgID == orgA.OrgID {
		t.Errorf("tenant B's call returned tenant A's organization (%q) — cross-tenant leak", orgA.OrgID)
	}
	if gotB.TenantID != tnB.TenantID {
		t.Errorf("tenant B got tenant_id = %q, want its own %q", gotB.TenantID, tnB.TenantID)
	}
}

// TestTenantOrgAPI_NoPermAdminIsForbidden proves the PermAdmin gate: a
// read-only tenant principal is refused before the organization store is
// ever consulted, mirroring TestTenantUsers_Authorization's shape for the
// sibling route.
func TestTenantOrgAPI_NoPermAdminIsForbidden(t *testing.T) {
	h := realTenantOrgHarness(t)
	tn := h.tenantOrgTenant(t, "tenant-org-forbidden")
	h.createOrgFor(t, tn, "tenant-org-forbidden")

	rec := h.getAsRole(t, tn, []string{"oneops-reader"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// TestTenantOrgAPI_NoOrgForTenantIsNotFound proves the 404 path against the
// real store: a tenant registered with no organization row at all (a state
// the 1:1 constraint should prevent, but the handler must not panic if it is
// ever violated) gets a clean RFC 7807 404, not a 500.
func TestTenantOrgAPI_NoOrgForTenantIsNotFound(t *testing.T) {
	h := realTenantOrgHarness(t)
	tn := h.tenantOrgTenant(t, "tenant-org-missing")

	rec := h.get(t, tn)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
