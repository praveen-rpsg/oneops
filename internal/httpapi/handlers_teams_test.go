package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

type fakeTeams struct {
	creates, gets, lists, patches, adds, removes, memberLists int
	lastTenant                                                string
}

func (f *fakeTeams) Create(_ context.Context, t *domain.Team) (*domain.Team, error) {
	f.creates++
	f.lastTenant = t.TenantID
	return t, nil
}
func (f *fakeTeams) Get(context.Context, string) (*domain.Team, error) {
	f.gets++
	return &domain.Team{TeamID: "tm1", Status: domain.TeamActive, RowVersion: 1}, nil
}
func (f *fakeTeams) ListByOrg(context.Context, string, int, string) ([]*domain.Team, error) {
	f.lists++
	return nil, nil
}
func (f *fakeTeams) UpdateProfile(context.Context, string, int64, string) (*domain.Team, error) {
	f.patches++
	return &domain.Team{TeamID: "tm1", Status: domain.TeamActive, RowVersion: 2}, nil
}
func (f *fakeTeams) SetStatus(context.Context, string, int64, domain.TeamStatus) (*domain.Team, error) {
	f.patches++
	return &domain.Team{TeamID: "tm1", Status: domain.TeamArchived, RowVersion: 2}, nil
}
func (f *fakeTeams) AddMember(_ context.Context, m *domain.TeamMembership) (*domain.TeamMembership, error) {
	f.adds++
	f.lastTenant = m.TenantID
	return m, nil
}
func (f *fakeTeams) RemoveMember(context.Context, string, string) error {
	f.removes++
	return nil
}
func (f *fakeTeams) ListMembers(context.Context, string, int, string) ([]*domain.TeamMembership, error) {
	f.memberLists++
	return nil, nil
}
func (f *fakeTeams) touched() int {
	return f.creates + f.gets + f.lists + f.patches + f.adds + f.removes + f.memberLists
}

// Team is tenant-owned, so the authorization tier is tenant administration —
// not platform administration, exactly as membership's is (and for the same
// reason: requirePlatformAdmin requires the system tenant, which would confine
// the connection to rows no customer owns).
//
// Every refusal asserts the STORE WAS NEVER REACHED, not merely the status
// code: authorization that runs after the write is not authorization.
func TestTeams_Authorization(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/teams?org_id=o1", ""},
		{http.MethodPost, "/v1/admin/teams", `{"org_id":"o1","name":"Team"}`},
		{http.MethodGet, "/v1/admin/teams/tm1", ""},
		{http.MethodPatch, "/v1/admin/teams/tm1", `{"row_version":1,"name":"New"}`},
		{http.MethodGet, "/v1/admin/teams/tm1/members", ""},
		{http.MethodPost, "/v1/admin/teams/tm1/members", `{"user_id":"u1"}`},
		{http.MethodDelete, "/v1/admin/teams/tm1/members/u1", ""},
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
			t.Run(tc.name+" "+rt.method+" "+rt.path, func(t *testing.T) {
				srv, _ := newTestServer(true)
				srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
				spy := &fakeTeams{}
				srv.SetTeams(spy)

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
						"refuse before any team is read or written", rt.method, rt.path)
				}
			})
		}
	}
}

// The tenant administrator succeeds, so the refusals above are about
// authorization and not about a route that never works.
func TestTeams_TenantAdminIsAdmitted(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeTeams{}
	srv.SetTeams(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/teams",
		strings.NewReader(`{"org_id":"o1","name":"Platform Team"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("tenant administrator = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if spy.creates != 1 {
		t.Errorf("store reached %d time(s), want 1", spy.creates)
	}
	// THE ISOLATION ASSERTION: the tenant came from the resolved connection,
	// not from anything the caller supplied.
	if spy.lastTenant != "t-acme" {
		t.Errorf("team was created in tenant %q, want the caller's resolved tenant %q",
			spy.lastTenant, "t-acme")
	}
}

// A caller must not be able to name its own tenant. The request DTO carries no
// tenant field at all, so a body attempting one is ignored rather than
// honoured — the same guarantee memberships give.
func TestTeams_CallerCannotChooseItsTenant(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeTeams{}
	srv.SetTeams(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/teams",
		strings.NewReader(`{"org_id":"o1","name":"Team","tenant_id":"t-victim"}`))
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

func TestTeams_RefusesMalformedInput(t *testing.T) {
	for _, c := range []struct {
		name, method, path, body string
		want                     int
	}{
		{"list without org_id", http.MethodGet, "/v1/admin/teams", "", http.StatusUnprocessableEntity},
		{"list bad limit", http.MethodGet, "/v1/admin/teams?org_id=o1&limit=x", "", http.StatusUnprocessableEntity},
		{"create bad json", http.MethodPost, "/v1/admin/teams", `{`, http.StatusBadRequest},
		{"create missing name", http.MethodPost, "/v1/admin/teams", `{"org_id":"o1"}`, http.StatusUnprocessableEntity},
		{"patch missing row_version", http.MethodPatch, "/v1/admin/teams/tm1", `{"name":"x"}`, http.StatusUnprocessableEntity},
		{"patch neither field", http.MethodPatch, "/v1/admin/teams/tm1", `{"row_version":1}`, http.StatusUnprocessableEntity},
		{"patch both fields", http.MethodPatch, "/v1/admin/teams/tm1",
			`{"row_version":1,"name":"x","status":"archived"}`, http.StatusUnprocessableEntity},
		{"add member bad json", http.MethodPost, "/v1/admin/teams/tm1/members", `{`, http.StatusBadRequest},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := newTestServer(true)
			srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
			spy := &fakeTeams{}
			srv.SetTeams(spy)

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

// Removing a member returns 204 with no body, the same shape webhook and
// policy deletes use.
func TestTeams_RemoveMemberReturnsNoContent(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeTeams{}
	srv.SetTeams(spy)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/teams/tm1/members/u1", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if spy.removes != 1 {
		t.Errorf("store reached %d time(s), want 1", spy.removes)
	}
}

// 501 rather than 404 when the repository is unwired, so an operator sees a
// deliberate configuration gap rather than what looks like a routing mistake.
func TestTeams_NotConfiguredReports501(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/teams?org_id=o1", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
}
