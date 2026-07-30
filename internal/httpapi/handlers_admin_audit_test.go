package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

type fakeAdminAudit struct{ calls int }

func (f *fakeAdminAudit) QueryAdminAudit(
	context.Context, domain.AdminAuditFilter,
) ([]*domain.AdminAuditRecord, *domain.AdminAuditCursor, error) {
	f.calls++
	return nil, nil, nil
}

// Authorization proof for the constitutional read boundary. Administrative
// history is outside row-level security by ADR-AUDIT-007 §6.4, so this
// middleware and the privileged database role behind it ARE the isolation —
// every refusal below is load-bearing, and each asserts the store was never
// reached, not merely that the status code was 403.
func TestAdminAuditQuery_Authorization(t *testing.T) {
	const route = "/v1/admin/audit/events"

	cases := []struct {
		name   string
		token  func(t *testing.T) string
		want   int
		reason string
	}{
		{"no token at all", func(*testing.T) string { return "" }, http.StatusUnauthorized,
			"an unauthenticated caller must not reach administrative history"},
		{"tenant administrator", func(t *testing.T) string {
			return mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"})
		}, http.StatusForbidden,
			"tenant administration does not extend to the platform's administrative trail"},
		{"read-only principal", func(t *testing.T) string {
			return mintRoleTenantToken(t, "ext-acme", []string{"oneops-reader"})
		}, http.StatusForbidden, "PermRead is the lowest tier and must not reach this"},
		{"platform role but a customer tenant", func(t *testing.T) string {
			return mintRoleTenantToken(t, "ext-acme", []string{"oneops-platform-admin"})
		}, http.StatusForbidden,
			"the role alone is insufficient: requirePlatformAdmin also requires the system tenant"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newTestServer(true)
			srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
			spy := &fakeAdminAudit{}
			srv.SetAdminAudit(spy)

			req := httptest.NewRequest(http.MethodGet, route, nil)
			if tok := tc.token(t); tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Errorf("%s: status = %d, want %d (%s)", tc.name, rec.Code, tc.want, tc.reason)
			}
			if spy.calls != 0 {
				t.Errorf("%s: the store was reached %d time(s); authorization must refuse before "+
					"any administrative history is read", tc.name, spy.calls)
			}
		})
	}
}

// The authorized caller succeeds, so the refusals above are proven to be about
// authorization rather than a route that never works.
func TestAdminAuditQuery_PlatformAdminIsAdmitted(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAdminAudit{}
	srv.SetAdminAudit(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/audit/events", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "", []string{"oneops-platform-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("platform administrator = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if spy.calls != 1 {
		t.Errorf("store reached %d time(s), want exactly 1", spy.calls)
	}
}

// Malformed input is refused before the store, so a caller cannot probe the
// query surface with values it was never designed to accept.
func TestAdminAuditQuery_RefusesMalformedInput(t *testing.T) {
	for _, q := range []string{"?limit=abc", "?from=yesterday", "?after=not-a-cursor", "?operation=nope"} {
		t.Run(q, func(t *testing.T) {
			srv, _ := newTestServer(true)
			srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
			spy := &fakeAdminAudit{}
			srv.SetAdminAudit(spy)

			req := httptest.NewRequest(http.MethodGet, "/v1/admin/audit/events"+q, nil)
			req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "", []string{"oneops-platform-admin"}))
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Errorf("%s: status = %d, want 422", q, rec.Code)
			}
		})
	}
}
