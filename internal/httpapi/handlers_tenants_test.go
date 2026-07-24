package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/rpsg/oneops/internal/domain"
)

// fakeTenants is an in-memory TenantRepository. It records lookups so the tests
// can assert that the authentication boundary consults the registry rather than
// trusting the claim.
type fakeTenants struct {
	byExternal map[string]*domain.Tenant
	byID       map[string]*domain.Tenant
	lookups    int
	failWith   error
}

func newFakeTenants(ts ...*domain.Tenant) *fakeTenants {
	f := &fakeTenants{
		byExternal: map[string]*domain.Tenant{},
		byID:       map[string]*domain.Tenant{},
	}
	for _, t := range ts {
		f.byID[t.TenantID] = t
		if t.ExternalID != "" {
			f.byExternal[t.ExternalID] = t
		}
	}
	return f
}

func (f *fakeTenants) Create(_ context.Context, t *domain.Tenant) (*domain.Tenant, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	for _, e := range f.byID {
		if e.Slug == t.Slug || (t.ExternalID != "" && e.ExternalID == t.ExternalID) {
			return nil, domain.ErrConflict
		}
	}
	cp := *t
	cp.RowVersion = 1
	f.byID[cp.TenantID] = &cp
	if cp.ExternalID != "" {
		f.byExternal[cp.ExternalID] = &cp
	}
	return &cp, nil
}

func (f *fakeTenants) Get(_ context.Context, id string) (*domain.Tenant, error) {
	if t, ok := f.byID[id]; ok {
		return t, nil
	}
	return nil, domain.ErrNotFound
}

func (f *fakeTenants) GetByExternalID(_ context.Context, ext string) (*domain.Tenant, error) {
	f.lookups++
	if f.failWith != nil {
		return nil, f.failWith
	}
	if t, ok := f.byExternal[ext]; ok {
		return t, nil
	}
	return nil, domain.ErrNotFound
}

func (f *fakeTenants) List(_ context.Context) ([]*domain.Tenant, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	out := make([]*domain.Tenant, 0, len(f.byID))
	for _, t := range f.byID {
		out = append(out, t)
	}
	return out, nil
}

func (f *fakeTenants) SetStatus(
	_ context.Context, id string, rv int64, st domain.TenantStatus,
) (*domain.Tenant, error) {
	t, ok := f.byID[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if t.RowVersion != rv {
		return nil, domain.ErrVersionMismatch
	}
	t.Status = st
	t.RowVersion++
	return t, nil
}

func activeTenant(id, ext, slug string) *domain.Tenant {
	return &domain.Tenant{
		TenantID: id, ExternalID: ext, Slug: slug, Name: slug,
		Status: domain.TenantActive, RowVersion: 1,
	}
}

// --- Authentication-boundary behaviour -------------------------------------
//
// These are the tests that give the registry its value. A tenant claim is
// data supplied by a token; the platform must resolve it against the registry
// and reject what it does not recognise, or "tenancy" is only a column.

func TestTenantClaimMustBeRegistered(t *testing.T) {
	tenants := newFakeTenants(activeTenant("t-acme", "ext-acme", "acme"))
	srv, _ := newTestServer(true)
	srv.SetTenants(tenants)

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil)
	req.Header.Set("Authorization", "Bearer "+mintTenantToken(t, "ext-unknown"))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("unregistered tenant = %d, want 403", rec.Code)
	}
	if tenants.lookups == 0 {
		t.Error("the registry was not consulted — the claim was trusted")
	}
	// The rejection must not confirm which tenant identifiers exist.
	if strings.Contains(rec.Body.String(), "ext-unknown") {
		t.Errorf("response echoed the rejected tenant claim: %s", rec.Body.String())
	}
}

func TestSuspendedTenantIsRejected(t *testing.T) {
	suspended := activeTenant("t-old", "ext-old", "old")
	suspended.Status = domain.TenantSuspended
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(suspended))

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil)
	req.Header.Set("Authorization", "Bearer "+mintTenantToken(t, "ext-old"))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("suspended tenant = %d, want 403", rec.Code)
	}
}

func TestRegisteredTenantIsAdmitted(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil)
	req.Header.Set("Authorization", "Bearer "+mintTenantToken(t, "ext-acme"))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("registered tenant = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// A token asserting no tenant resolves to the system tenant. This is what keeps
// existing single-tenant deployments working after tenancy is introduced.
func TestNoTenantClaimResolvesToSystem(t *testing.T) {
	tenants := newFakeTenants(activeTenant("t-acme", "ext-acme", "acme"))
	srv, _ := newTestServer(true)
	srv.SetTenants(tenants)

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil)
	req.Header.Set("Authorization", "Bearer "+mintTenantToken(t, ""))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("no tenant claim = %d, want 200", rec.Code)
	}
	if tenants.lookups != 0 {
		t.Error("an absent claim should not trigger a registry lookup")
	}
}

// Without a registry the platform cannot validate a claim. It must behave as it
// did before tenancy rather than invent an unverified boundary.
func TestUnwiredRegistryResolvesToSystem(t *testing.T) {
	srv, _ := newTestServer(true)

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil)
	req.Header.Set("Authorization", "Bearer "+mintTenantToken(t, "ext-whatever"))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unwired registry = %d, want 200", rec.Code)
	}
}

// A registry that is unreachable must fail the request closed. Falling back to
// the system tenant on a database error would silently route one tenant's
// traffic into another's data during an outage.
func TestTenantLookupFailureIsRejected(t *testing.T) {
	tenants := newFakeTenants(activeTenant("t-acme", "ext-acme", "acme"))
	tenants.failWith = errors.New("connection refused")
	srv, _ := newTestServer(true)
	srv.SetTenants(tenants)

	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil)
	req.Header.Set("Authorization", "Bearer "+mintTenantToken(t, "ext-acme"))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("registry failure = %d, want 500", rec.Code)
	}
	// The driver message must not reach the caller.
	if strings.Contains(rec.Body.String(), "connection refused") {
		t.Errorf("response leaked the internal cause: %s", rec.Body.String())
	}
}

// --- Registry administration ------------------------------------------------

func TestCreateTenant(t *testing.T) {
	srv, _ := newTestServer(false)
	srv.SetTenants(newFakeTenants())

	body := `{"slug":"acme","name":"Acme Corp","external_id":"ext-acme"}`
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/v1/admin/tenants", strings.NewReader(body)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var got tenantDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Slug != "acme" || got.Status != "active" {
		t.Fatalf("unexpected tenant: %+v", got)
	}
	// The id is minted server-side so a caller cannot collide with "system".
	if got.TenantID == "" || got.TenantID == domain.SystemTenantID {
		t.Errorf("tenant id should be server-minted and distinct: %q", got.TenantID)
	}
}

func TestCreateTenantRejectsBadSlug(t *testing.T) {
	srv, _ := newTestServer(false)
	srv.SetTenants(newFakeTenants())

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/v1/admin/tenants",
		strings.NewReader(`{"slug":"Acme Corp!","name":"Acme"}`)))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad slug = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateTenantDuplicateIsConflict(t *testing.T) {
	srv, _ := newTestServer(false)
	srv.SetTenants(newFakeTenants(activeTenant("t1", "ext-1", "acme")))

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(
		http.MethodPost, "/v1/admin/tenants",
		strings.NewReader(`{"slug":"acme","name":"Acme"}`)))

	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate slug = %d, want 409", rec.Code)
	}
}

func TestSuspendTenant(t *testing.T) {
	srv, _ := newTestServer(false)
	srv.SetTenants(newFakeTenants(activeTenant("t1", "ext-1", "acme")))

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(
		http.MethodPatch, "/v1/admin/tenants/t1",
		strings.NewReader(`{"status":"suspended"}`)))

	if rec.Code != http.StatusOK {
		t.Fatalf("suspend = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got tenantDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Status != "suspended" {
		t.Errorf("status = %q, want suspended", got.Status)
	}
}

// Suspending the system tenant would lock the platform out of every
// pre-tenancy row, with no route back in through the API.
func TestSystemTenantCannotBeSuspended(t *testing.T) {
	srv, _ := newTestServer(false)
	srv.SetTenants(newFakeTenants(
		activeTenant(domain.SystemTenantID, "system", "system")))

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(
		http.MethodPatch, "/v1/admin/tenants/"+domain.SystemTenantID,
		strings.NewReader(`{"status":"suspended"}`)))

	if rec.Code != http.StatusConflict {
		t.Fatalf("suspend system = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

func TestPatchTenantUnknownIsNotFound(t *testing.T) {
	srv, _ := newTestServer(false)
	srv.SetTenants(newFakeTenants())

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(
		http.MethodPatch, "/v1/admin/tenants/nope",
		strings.NewReader(`{"status":"suspended"}`)))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown tenant = %d, want 404", rec.Code)
	}
}

func TestPatchTenantInvalidStatus(t *testing.T) {
	srv, _ := newTestServer(false)
	srv.SetTenants(newFakeTenants(activeTenant("t1", "ext-1", "acme")))

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(
		http.MethodPatch, "/v1/admin/tenants/t1",
		strings.NewReader(`{"status":"deleted"}`)))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad status = %d, want 422", rec.Code)
	}
}

func TestListTenants(t *testing.T) {
	srv, _ := newTestServer(false)
	srv.SetTenants(newFakeTenants(
		activeTenant("t1", "ext-1", "acme"),
		activeTenant("t2", "ext-2", "globex")))

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/admin/tenants", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", rec.Code)
	}
	var body struct {
		Items []tenantDTO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 2 {
		t.Errorf("items = %d, want 2", len(body.Items))
	}
}

// The registry routes must degrade explicitly rather than panic on a nil
// dependency, matching how every other optional subsystem behaves.
func TestTenantRoutesWithoutRegistry(t *testing.T) {
	srv, _ := newTestServer(false)

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/tenants", ""},
		{http.MethodPost, "/v1/admin/tenants", `{"slug":"x","name":"X"}`},
		{http.MethodPatch, "/v1/admin/tenants/t1", `{"status":"active"}`},
	} {
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, httptest.NewRequest(
			tc.method, tc.path, strings.NewReader(tc.body)))
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s = %d, want 501", tc.method, tc.path, rec.Code)
		}
	}
}

// mintRoleTenantToken mints a token asserting both a tenant and arbitrary
// roles, which is what a customer's own identity provider can do.
func mintRoleTenantToken(t *testing.T, external string, roles []string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub": "u", "iss": tIss, "aud": tAud,
		"exp": time.Now().Add(time.Hour).Unix(), "roles": roles,
	}
	if external != "" {
		claims["tenant"] = external
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(tSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A tenant administrator must not reach the tenant registry.
//
// Against the running service, one could list every customer's slug and
// external identifier, register tenants binding identifiers of its choosing,
// and suspend a different customer — which locked that customer out of the
// platform entirely (403 "tenant is suspended" on every request).
func TestTenantAdminCannotAdministerTenants(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(
		activeTenant("t-acme", "ext-acme", "acme"),
		activeTenant("t-globex", "ext-globex", "globex"),
	))
	token := mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"})

	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/tenants", ""},
		{http.MethodPost, "/v1/admin/tenants", `{"slug":"shadow","name":"Shadow"}`},
		{http.MethodPatch, "/v1/admin/tenants/t-globex", `{"status":"suspended"}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403 for a tenant administrator",
				tc.method, tc.path, rec.Code)
		}
	}
}

// Roles arrive in a bearer token, so a customer's identity provider can assert
// the platform role. The tenant cannot be forged the same way: it is resolved
// against the registry. Platform operations therefore require both, and this
// is the case that proves the second condition carries its own weight.
func TestForgedPlatformRoleFromATenantIsRefused(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenants", nil)
	req.Header.Set("Authorization", "Bearer "+
		mintRoleTenantToken(t, "ext-acme", []string{"oneops-platform-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("a tenant-scoped caller asserting the platform role = %d, want 403", rec.Code)
	}
	// The refusal must not disclose that the roles were sufficient and only the
	// tenant was wrong, which would tell an attacker exactly what to change.
	if strings.Contains(strings.ToLower(rec.Body.String()), "tenant") {
		t.Errorf("the refusal distinguishes the failing condition: %s", rec.Body.String())
	}
}

// Resolving to the system tenant is the default for a token asserting no
// tenant, so that condition alone must not authorise platform administration.
func TestSystemTenantWithoutPlatformRoleIsRefused(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants())

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenants", nil)
	req.Header.Set("Authorization", "Bearer "+
		mintRoleTenantToken(t, "", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("system tenant without the platform role = %d, want 403", rec.Code)
	}
}

// The operator, holding both conditions, still works.
func TestPlatformAdminOnSystemTenantIsAdmitted(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenants", nil)
	req.Header.Set("Authorization", "Bearer "+
		mintRoleTenantToken(t, "", []string{"oneops-platform-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("platform administrator = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// Tenant-scoped administration is unaffected: a tenant administrator still
// administers its own subsystems, where row-level security confines the result.
func TestTenantAdminRetainsTenantScopedAdministration(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/webhooks", nil)
	req.Header.Set("Authorization", "Bearer "+
		mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	// Not wired in this harness, so 501 — the point is that authorization let
	// it through rather than refusing with 403.
	if rec.Code == http.StatusForbidden {
		t.Fatal("a tenant administrator lost administration inside its own tenant")
	}
}
