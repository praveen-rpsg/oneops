package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

type fakeMemberships struct {
	grants, revokes, lists int
	lastTenant             string
}

func (f *fakeMemberships) Grant(_ context.Context, m *domain.Membership) (*domain.Membership, error) {
	f.grants++
	f.lastTenant = m.TenantID
	m.Status = domain.MembershipActive
	return m, nil
}
func (f *fakeMemberships) Revoke(context.Context, string) (*domain.Membership, error) {
	f.revokes++
	return &domain.Membership{MembershipID: "m1", Status: domain.MembershipRevoked}, nil
}
func (f *fakeMemberships) ListByOrg(context.Context, string, int, string) ([]*domain.Membership, error) {
	f.lists++
	return nil, nil
}
func (f *fakeMemberships) touched() int { return f.grants + f.revokes + f.lists }

// Membership is tenant-owned, so the authorization tier is tenant
// administration — not platform administration, which would be the wrong
// control and, because it requires the system tenant, would also be confined by
// row-level security to rows no customer owns.
//
// Every refusal asserts the STORE WAS NEVER REACHED, not merely the status
// code: authorization that runs after the write is not authorization.
func TestMemberships_Authorization(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/memberships?org_id=o1", ""},
		{http.MethodPost, "/v1/admin/memberships", `{"org_id":"o1","user_id":"u1"}`},
		{http.MethodDelete, "/v1/admin/memberships/m1", ""},
	}
	cases := []struct {
		name  string
		token func(t *testing.T) string
		want  int
	}{
		{"no token", func(*testing.T) string { return "" }, http.StatusUnauthorized},
		{"read-only principal", func(t *testing.T) string {
			return mintRoleTenantToken(t, "ext-acme", []string{"oneops-reader"})
		}, http.StatusForbidden},
	}

	for _, tc := range cases {
		for _, rt := range routes {
			t.Run(tc.name+" "+rt.method, func(t *testing.T) {
				srv, _ := newTestServer(true)
				srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
				spy := &fakeMemberships{}
				srv.SetMemberships(spy)

				body := strings.NewReader(rt.body)
				req := httptest.NewRequest(rt.method, rt.path, body)
				if tok := tc.token(t); tok != "" {
					req.Header.Set("Authorization", "Bearer "+tok)
				}
				rec := httptest.NewRecorder()
				srv.Router().ServeHTTP(rec, req)

				if rec.Code != tc.want {
					t.Errorf("%s %s = %d, want %d", rt.method, rt.path, rec.Code, tc.want)
				}
				if spy.touched() != 0 {
					t.Errorf("%s %s: the store was reached despite refusal — authorization must "+
						"refuse before any membership is read or written", rt.method, rt.path)
				}
			})
		}
	}
}

// The tenant administrator succeeds, so the refusals above are about
// authorization and not about a route that never works.
func TestMemberships_TenantAdminIsAdmitted(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeMemberships{}
	srv.SetMemberships(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/memberships",
		strings.NewReader(`{"org_id":"o1","user_id":"u1"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("tenant administrator = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if spy.grants != 1 {
		t.Errorf("store reached %d time(s), want 1", spy.grants)
	}
	// THE ISOLATION ASSERTION: the tenant came from the resolved connection,
	// not from anything the caller supplied.
	if spy.lastTenant != "t-acme" {
		t.Errorf("membership was granted in tenant %q, want the caller's resolved tenant %q",
			spy.lastTenant, "t-acme")
	}
}

// A caller must not be able to name its own tenant. The request DTO carries no
// tenant field at all, so a body attempting one is ignored rather than honoured.
func TestMemberships_CallerCannotChooseItsTenant(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeMemberships{}
	srv.SetMemberships(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/memberships",
		strings.NewReader(`{"org_id":"o1","user_id":"u1","tenant_id":"t-victim"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if spy.lastTenant != "t-acme" {
		t.Errorf("a caller-supplied tenant_id was honoured (%q) — cross-tenant key confusion",
			spy.lastTenant)
	}
}

// Malformed input is refused before the store.
func TestMemberships_RefusesMalformedInput(t *testing.T) {
	for _, c := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"list without org_id", http.MethodGet, "/v1/admin/memberships", "", http.StatusUnprocessableEntity},
		{"list bad limit", http.MethodGet, "/v1/admin/memberships?org_id=o1&limit=x", "", http.StatusUnprocessableEntity},
		{"grant bad json", http.MethodPost, "/v1/admin/memberships", `{`, http.StatusBadRequest},
		{"grant missing user", http.MethodPost, "/v1/admin/memberships", `{"org_id":"o1"}`, http.StatusUnprocessableEntity},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := newTestServer(true)
			srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
			spy := &fakeMemberships{}
			srv.SetMemberships(spy)

			req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
			req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if rec.Code != c.want {
				t.Errorf("%s = %d, want %d: %s", c.name, rec.Code, c.want, rec.Body.String())
			}
			if spy.touched() != 0 {
				t.Errorf("%s: the store was reached on malformed input", c.name)
			}
		})
	}
}
