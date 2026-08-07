//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
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

// invitationsHarness wires all three /admin/invitations routes against the
// REAL InvitationStore and OrganizationStore — both over an appPool
// (NewTenantScopedPool), exactly the composition cmd/controlplane/main.go
// builds them over — plus a privileged pool used ONLY to register tenants and
// insert organization fixtures, never for anything the endpoints themselves
// read. This is the wiring the handler unit tests (fakes,
// handlers_invitations_test.go) cannot prove: that row-level security, not a
// predicate the query happens to carry, is what confines one tenant's
// invitations from another's (invitation is TENANT-OWNED — ADR-IDENTITY-002
// §2.4).
type invitationsHarness struct {
	router      http.Handler
	dsn         string
	priv        *pgxpool.Pool
	appPool     *pgxpool.Pool
	tenants     *postgres.TenantStore
	invitations *postgres.InvitationStore
}

func realInvitationsHarness(t *testing.T) *invitationsHarness {
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

	h := &invitationsHarness{
		dsn: dsn, priv: priv, appPool: appPool,
		tenants:     postgres.NewTenantStore(priv),
		invitations: postgres.NewInvitationStore(appPool),
	}
	s.SetTenants(h.tenants)
	// The same wiring cmd/controlplane/main.go uses: OrganizationStore and
	// InvitationStore both over the tenant-scoped appPool.
	s.SetOrganizations(postgres.NewOrganizationStore(appPool))
	s.SetInvitations(h.invitations)
	h.router = s.Router()
	return h
}

// invitationsTenant registers a real tenant on the privileged pool.
func (h *invitationsHarness) invitationsTenant(t *testing.T, slug string) *domain.Tenant {
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
// tenant, directly via the privileged pool — the same minimal fixture
// tenant_org_integration_test.go's createOrgFor uses, for the same reason:
// OrganizationStore.Create mints its OWN tenant row, which would collide with
// the tenant this harness already registered with a resolvable ExternalID.
func (h *invitationsHarness) createOrgFor(t *testing.T, tn *domain.Tenant) string {
	t.Helper()
	orgID := domain.NewID()
	if _, err := h.priv.Exec(context.Background(), `
		INSERT INTO organization (org_id, tenant_id, slug, name, status)
		VALUES ($1, $2, $3, $3, 'active')`,
		orgID, tn.TenantID, uniqueSlug("org")); err != nil {
		t.Fatalf("create organization for tenant %s: %v", tn.TenantID, err)
	}
	return orgID
}

// create issues an authenticated POST /v1/admin/invitations as tn's admin.
// body carries whatever the caller wants to attempt to smuggle; the DTO the
// handler decodes into has no org_id/tenant_id field, so any such key is a
// no-op at the JSON layer — the assertion this test suite makes is that the
// RESPONSE still names tn's own organization regardless.
func (h *invitationsHarness) create(t *testing.T, tn *domain.Tenant, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/invitations", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, tn.ExternalID, []string{"oneops-admin"}))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h *invitationsHarness) createAsRole(t *testing.T, tn *domain.Tenant, roles []string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/invitations", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, tn.ExternalID, roles))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// list issues an authenticated GET /v1/admin/invitations as tn's admin.
func (h *invitationsHarness) list(t *testing.T, tn *domain.Tenant) (*httptest.ResponseRecorder, []invitationDTO) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/invitations", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, tn.ExternalID, []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	var items []invitationDTO
	if rec.Code == http.StatusOK {
		items = decodeInvitationList(t, rec)
	}
	return rec, items
}

func (h *invitationsHarness) listAsRole(t *testing.T, tn *domain.Tenant, roles []string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/invitations", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, tn.ExternalID, roles))
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// revoke issues an authenticated DELETE /v1/admin/invitations/{id} as tn's
// admin.
func (h *invitationsHarness) revoke(t *testing.T, tn *domain.Tenant, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/invitations/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, tn.ExternalID, []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h *invitationsHarness) revokeAsRole(t *testing.T, tn *domain.Tenant, roles, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/invitations/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, tn.ExternalID, []string{roles}))
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// TestInvitationsAPI_CreateLandsInCallersOwnOrgAndReturnsTokenOnce is the
// correctness proof against the REAL store: the invitation lands under the
// caller's own organization, the create response carries a non-empty
// one-time token, and a subsequent read (list) never exposes it.
func TestInvitationsAPI_CreateLandsInCallersOwnOrgAndReturnsTokenOnce(t *testing.T) {
	h := realInvitationsHarness(t)
	tn := h.invitationsTenant(t, "inv-correctness")
	orgID := h.createOrgFor(t, tn)

	rec := h.create(t, tn, `{"email":"invitee@example.com"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var created createInvitationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v (body %s)", err, rec.Body.String())
	}
	if created.OrgID != orgID {
		t.Errorf("org_id = %q, want the caller's own %q", created.OrgID, orgID)
	}
	if created.Email != "invitee@example.com" {
		t.Errorf("email = %q, want the normalised address", created.Email)
	}
	if created.Status != string(domain.InvitationPending) {
		t.Errorf("status = %q, want pending", created.Status)
	}
	if created.Token == "" {
		t.Fatal("create response carries no one-time token")
	}
	if strings.Contains(rec.Body.String(), "token_hash") {
		t.Error("create response leaked token_hash")
	}

	// A second read (list) must never expose the token, in any shape.
	_, items := h.list(t, tn)
	if len(items) != 1 || items[0].InvitationID != created.InvitationID {
		t.Fatalf("list = %+v, want exactly the created invitation", items)
	}
	listBody, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal list items: %v", err)
	}
	if strings.Contains(string(listBody), created.Token) {
		t.Error("the plaintext token leaked into the list response")
	}
}

// TestInvitationsAPI_TenantIsolation is the security-critical property this
// story exists to prove: a PermAdmin in tenant A cannot see tenant B's
// invitations, and A's own create always lands in A's org even when the
// request body tries to smuggle a different org_id — the smuggle attempt is
// silently ignored because createInvitationRequest has no such field.
func TestInvitationsAPI_TenantIsolation(t *testing.T) {
	h := realInvitationsHarness(t)
	tnA := h.invitationsTenant(t, "inv-iso-a")
	tnB := h.invitationsTenant(t, "inv-iso-b")
	orgA := h.createOrgFor(t, tnA)
	orgB := h.createOrgFor(t, tnB)

	// A's create attempts to smuggle B's org_id; the response must still name
	// A's own organization.
	recA := h.create(t, tnA, `{"email":"a-invitee@example.com","org_id":"`+orgB+`","tenant_id":"`+tnB.TenantID+`"}`)
	if recA.Code != http.StatusCreated {
		t.Fatalf("tenant A create: status = %d, want 201: %s", recA.Code, recA.Body.String())
	}
	var createdA createInvitationResponse
	if err := json.Unmarshal(recA.Body.Bytes(), &createdA); err != nil {
		t.Fatalf("decode tenant A create response: %v", err)
	}
	if createdA.OrgID != orgA {
		t.Errorf("tenant A's invitation landed under org_id %q, want its own %q (smuggled org_id was honoured)",
			createdA.OrgID, orgA)
	}
	if createdA.OrgID == orgB {
		t.Error("tenant A's invitation landed under tenant B's organization — cross-tenant key confusion")
	}

	recB := h.create(t, tnB, `{"email":"b-invitee@example.com"}`)
	if recB.Code != http.StatusCreated {
		t.Fatalf("tenant B create: status = %d, want 201: %s", recB.Code, recB.Body.String())
	}
	var createdB createInvitationResponse
	if err := json.Unmarshal(recB.Body.Bytes(), &createdB); err != nil {
		t.Fatalf("decode tenant B create response: %v", err)
	}

	// Tenant A's list must contain EXACTLY A's own invitation, never B's.
	_, itemsA := h.list(t, tnA)
	if len(itemsA) != 1 || itemsA[0].InvitationID != createdA.InvitationID {
		t.Fatalf("tenant A's list = %+v, want exactly its own invitation", itemsA)
	}
	for _, it := range itemsA {
		if it.InvitationID == createdB.InvitationID {
			t.Error("tenant A's list leaked tenant B's invitation")
		}
	}

	// Tenant B's list must contain EXACTLY B's own invitation, never A's.
	_, itemsB := h.list(t, tnB)
	if len(itemsB) != 1 || itemsB[0].InvitationID != createdB.InvitationID {
		t.Fatalf("tenant B's list = %+v, want exactly its own invitation", itemsB)
	}
	for _, it := range itemsB {
		if it.InvitationID == createdA.InvitationID {
			t.Error("tenant B's list leaked tenant A's invitation")
		}
	}

	// Tenant B must not be able to revoke tenant A's invitation: the row is
	// invisible under tenant B's row-level-security boundary, so this is a
	// 404, never a 403 that would confirm A's invitation exists.
	crossRevoke := h.revoke(t, tnB, createdA.InvitationID)
	if crossRevoke.Code != http.StatusNotFound {
		t.Fatalf("tenant B revoking tenant A's invitation: status = %d, want 404: %s",
			crossRevoke.Code, crossRevoke.Body.String())
	}

	// Prove the invitation was NOT revoked by the cross-tenant attempt: tenant
	// A can still see it as pending.
	_, itemsAAfter := h.list(t, tnA)
	if len(itemsAAfter) != 1 || itemsAAfter[0].Status != string(domain.InvitationPending) {
		t.Fatalf("tenant A's invitation after a refused cross-tenant revoke = %+v, want still pending",
			itemsAAfter)
	}
}

// TestInvitationsAPI_NoPermAdminIsForbidden proves the PermAdmin gate on all
// three routes: a read-only tenant principal is refused before the
// invitation store is ever consulted.
func TestInvitationsAPI_NoPermAdminIsForbidden(t *testing.T) {
	h := realInvitationsHarness(t)
	tn := h.invitationsTenant(t, "inv-forbidden")
	h.createOrgFor(t, tn)

	if rec := h.createAsRole(t, tn, []string{"oneops-reader"}, `{"email":"a@example.com"}`); rec.Code != http.StatusForbidden {
		t.Errorf("create as reader: status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if rec := h.listAsRole(t, tn, []string{"oneops-reader"}); rec.Code != http.StatusForbidden {
		t.Errorf("list as reader: status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if rec := h.revokeAsRole(t, tn, "oneops-reader", "inv_missing"); rec.Code != http.StatusForbidden {
		t.Errorf("revoke as reader: status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
}

// TestInvitationsAPI_RevokeIsIdempotentSafe proves revoke's status predicate:
// the first call withdraws a pending invitation, the second finds it no
// longer pending and reports a conflict rather than silently succeeding
// again or panicking.
func TestInvitationsAPI_RevokeIsIdempotentSafe(t *testing.T) {
	h := realInvitationsHarness(t)
	tn := h.invitationsTenant(t, "inv-revoke")
	h.createOrgFor(t, tn)

	rec := h.create(t, tn, `{"email":"revoke-me@example.com"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var created createInvitationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	first := h.revoke(t, tn, created.InvitationID)
	if first.Code != http.StatusOK {
		t.Fatalf("first revoke: status = %d, want 200: %s", first.Code, first.Body.String())
	}
	var revoked invitationDTO
	if err := json.Unmarshal(first.Body.Bytes(), &revoked); err != nil {
		t.Fatalf("decode revoke response: %v", err)
	}
	if revoked.Status != string(domain.InvitationRevoked) {
		t.Errorf("status after first revoke = %q, want revoked", revoked.Status)
	}

	second := h.revoke(t, tn, created.InvitationID)
	if second.Code != http.StatusConflict {
		t.Fatalf("second revoke: status = %d, want 409: %s", second.Code, second.Body.String())
	}
}

// TestInvitationsAPI_RevokeUnknownIsNotFound proves the plain not-found path,
// distinct from the cross-tenant 404 proven inside the isolation test above.
func TestInvitationsAPI_RevokeUnknownIsNotFound(t *testing.T) {
	h := realInvitationsHarness(t)
	tn := h.invitationsTenant(t, "inv-missing")
	h.createOrgFor(t, tn)

	rec := h.revoke(t, tn, domain.NewID())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
