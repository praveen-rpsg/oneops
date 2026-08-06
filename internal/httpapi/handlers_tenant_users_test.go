package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// fakeTenantUserDirectory is the tenantUserDirectory fake: it records the
// limit/after it was called with, so pagination pass-through is provable
// against a fake alone, and a handler test never needs real Postgres to
// prove the transport does what it claims.
type fakeTenantUserDirectory struct {
	calls    int
	gotLimit int
	gotAfter string
	entries  []*domain.TenantUserSummary
	err      error
}

func (f *fakeTenantUserDirectory) ListActiveDirectory(
	_ context.Context, limit int, after string,
) ([]*domain.TenantUserSummary, error) {
	f.calls++
	f.gotLimit = limit
	f.gotAfter = after
	if f.err != nil {
		return nil, f.err
	}
	return f.entries, nil
}

var _ tenantUserDirectory = (*fakeTenantUserDirectory)(nil)

// TestTenantUsers_NotImplementedWhenUnwired proves the 501-until-configured
// convention every other administration surface in this package follows.
func TestTenantUsers_NotImplementedWhenUnwired(t *testing.T) {
	srv, _ := newTestServer(false)
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-users", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
}

// TestTenantUsers_AssemblesDTO proves every response field is traced to a
// specific fake return value — not merely "some JSON came back".
func TestTenantUsers_AssemblesDTO(t *testing.T) {
	srv, _ := newTestServer(false)
	spy := &fakeTenantUserDirectory{entries: []*domain.TenantUserSummary{
		{UserID: "u1", Email: "a@example.com", DisplayName: "Ada"},
		{UserID: "u2", Email: "b@example.com", DisplayName: "Bob"},
	}}
	srv.SetTenantUserDirectory(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-users", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Items []tenantUserDTO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2: %+v", len(got.Items), got.Items)
	}
	if got.Items[0] != (tenantUserDTO{UserID: "u1", Email: "a@example.com", DisplayName: "Ada"}) {
		t.Errorf("item[0] = %+v, want the fake's first entry verbatim", got.Items[0])
	}
	if got.Items[1] != (tenantUserDTO{UserID: "u2", Email: "b@example.com", DisplayName: "Bob"}) {
		t.Errorf("item[1] = %+v, want the fake's second entry verbatim", got.Items[1])
	}
	// The DTO is deliberately narrower than userDTO: no status, row_version or
	// timestamps travel over this route.
	if got := rec.Body.String(); containsAny(got, "row_version", "\"status\"", "created_at") {
		t.Errorf("body = %s, want no status/row_version/created_at — this is a narrower "+
			"projection than GET /admin/users, not the User aggregate", got)
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if len(sub) > 0 && len(s) >= len(sub) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}

// TestTenantUsers_EmptyTenantReturnsCleanEmptyList proves the empty-tenant
// bound: no members at all is a clean 200 with items: [], never a 500 and
// never a null array.
func TestTenantUsers_EmptyTenantReturnsCleanEmptyList(t *testing.T) {
	srv, _ := newTestServer(false)
	srv.SetTenantUserDirectory(&fakeTenantUserDirectory{entries: nil})

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-users", nil)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if want := `"items":[]`; !jsonContains(rec.Body.String(), want) {
		t.Errorf("body = %s, want items rendered as [], not null", rec.Body.String())
	}
}

func jsonContains(body, want string) bool {
	// Cheap substring check; the handler always writes a compact {"items":...}
	// map so this is stable without pulling in a JSON-diff dependency.
	for i := 0; i+len(want) <= len(body); i++ {
		if body[i:i+len(want)] == want {
			return true
		}
	}
	return false
}

// TestTenantUsers_LimitPassesThroughAndValidates proves the pagination
// parameters reach the store unmodified, and that a malformed limit is
// refused as a validation error rather than reaching the store at all.
func TestTenantUsers_LimitPassesThroughAndValidates(t *testing.T) {
	t.Run("valid limit and after pass through", func(t *testing.T) {
		srv, _ := newTestServer(false)
		spy := &fakeTenantUserDirectory{}
		srv.SetTenantUserDirectory(spy)

		req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-users?limit=10&after=u5", nil)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if spy.gotLimit != 10 || spy.gotAfter != "u5" {
			t.Errorf("store received limit=%d after=%q, want limit=10 after=%q",
				spy.gotLimit, spy.gotAfter, "u5")
		}
	})

	t.Run("non-positive limit is refused before reaching the store", func(t *testing.T) {
		srv, _ := newTestServer(false)
		spy := &fakeTenantUserDirectory{}
		srv.SetTenantUserDirectory(spy)

		req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-users?limit=0", nil)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)

		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
		}
		if spy.calls != 0 {
			t.Error("the store was reached despite an invalid limit")
		}
	})
}

// TestTenantUsers_Authorization is the tenant-administration authorization
// boundary this route deliberately shares with GET /admin/memberships, not
// the platform-admin gate GET /admin/users sits behind — a caller must hold
// PermAdmin, not requirePlatformAdmin. Every refusal asserts the store was
// never reached, not merely the status code.
func TestTenantUsers_Authorization(t *testing.T) {
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
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer(true)
			srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
			spy := &fakeTenantUserDirectory{}
			srv.SetTenantUserDirectory(spy)

			req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-users", nil)
			if tok := tc.token(t); tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if spy.calls != 0 {
				t.Error("the directory was reached despite refusal — authorization must run first")
			}
		})
	}

	// A tenant administrator (PermAdmin) IS admitted — the whole point of this
	// route existing separately from the platform-admin GET /admin/users.
	t.Run("tenant admin is admitted", func(t *testing.T) {
		srv, _ := newTestServer(true)
		srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
		spy := &fakeTenantUserDirectory{}
		srv.SetTenantUserDirectory(spy)

		req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-users", nil)
		req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if spy.calls != 1 {
			t.Errorf("directory calls = %d, want 1", spy.calls)
		}
	})
}
