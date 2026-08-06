package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---- not configured ---------------------------------------------------------

// An unwired registry answers 501, the same convention every other
// administration surface in this package uses for an unwired dependency.
func TestTenantOrganization_UnconfiguredRegistryIsNotImplemented(t *testing.T) {
	h := newOrgAPI(t, nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-org", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleToken(t, "oneops-admin"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
}

// ---- authorization ----------------------------------------------------------

// This route requires PermAdmin (tenant administration), not
// requirePlatformAdmin — it is the caller's OWN tenant's organization, not
// the global registry. Mirrors TestTenantUsers_Authorization.
func TestTenantOrganization_RequiresPermAdmin(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	org := seedOrg(t, "acme", "Acme")
	org.TenantID = "t-acme"
	repo := newFakeOrganizations(org)
	srv.SetOrganizations(repo)

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
			req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-org", nil)
			if tok := tc.token(t); tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}

	// A tenant administrator (PermAdmin) IS admitted.
	t.Run("tenant admin is admitted", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-org", nil)
		req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
		}
	})
}

// ---- resolution ---------------------------------------------------------

// TestTenantOrganization_ReturnsCallersOwnOrg proves the response is the
// caller's own tenant's organization, resolved by the context tenant, not by
// any input the request could carry.
func TestTenantOrganization_ReturnsCallersOwnOrg(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(
		activeTenant("t-acme", "ext-acme", "acme"),
		activeTenant("t-beta", "ext-beta", "beta"),
	))
	orgAcme := seedOrg(t, "acme-org", "Acme Org")
	orgAcme.TenantID = "t-acme"
	orgBeta := seedOrg(t, "beta-org", "Beta Org")
	orgBeta.TenantID = "t-beta"
	srv.SetOrganizations(newFakeOrganizations(orgAcme, orgBeta))

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-org", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := decodeOrg(t, rec)
	if got.OrgID != orgAcme.OrgID {
		t.Errorf("org_id = %q, want %q (acme's own organization, not beta's)", got.OrgID, orgAcme.OrgID)
	}
	if got.TenantID != "t-acme" {
		t.Errorf("tenant_id = %q, want t-acme", got.TenantID)
	}

	// The reverse caller sees the reverse organization — proves the handler
	// is not simply returning whichever org happens to be first/only.
	req2 := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-org", nil)
	req2.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-beta", []string{"oneops-admin"}))
	rec2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec2.Code, rec2.Body.String())
	}
	got2 := decodeOrg(t, rec2)
	if got2.OrgID != orgBeta.OrgID {
		t.Errorf("org_id = %q, want %q (beta's own organization, not acme's)", got2.OrgID, orgBeta.OrgID)
	}
}

// TestTenantOrganization_NoOrgForTenantIsNotFound proves the 1:1 gap is
// reported as 404, not a panic, if it is ever violated.
func TestTenantOrganization_NoOrgForTenantIsNotFound(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	srv.SetOrganizations(newFakeOrganizations())

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/tenant-org", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
