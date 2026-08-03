package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

type fakeCollectorChecks struct {
	creates, gets, lists, patches, deletes int
	lastTenant                             string
	lastCreated                            *domain.CollectorCheck
	lastPatch                              domain.CollectorCheckPatch
	createErr                              error
	updateErr                              error
	getErr                                 error
	deleteErr                              error
	getCheck                               *domain.CollectorCheck
}

func (f *fakeCollectorChecks) touched() int {
	return f.creates + f.gets + f.lists + f.patches + f.deletes
}

func (f *fakeCollectorChecks) Create(_ context.Context, c *domain.CollectorCheck) (*domain.CollectorCheck, error) {
	f.creates++
	f.lastTenant = c.TenantID
	f.lastCreated = c
	if f.createErr != nil {
		return nil, f.createErr
	}
	cp := *c
	cp.RowVersion = 1
	return &cp, nil
}

func (f *fakeCollectorChecks) Get(context.Context, string) (*domain.CollectorCheck, error) {
	f.gets++
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getCheck != nil {
		cp := *f.getCheck
		return &cp, nil
	}
	return &domain.CollectorCheck{
		CheckID: "c1", AssetID: "a1", Type: "http", Target: "https://example.test/health",
		IntervalSeconds: 60, Enabled: true, MetricPrefix: "chk", RowVersion: 1,
	}, nil
}

func (f *fakeCollectorChecks) List(context.Context, int, string) ([]*domain.CollectorCheck, error) {
	f.lists++
	return nil, nil
}

func (f *fakeCollectorChecks) Update(_ context.Context, _ string, _ int64, patch domain.CollectorCheckPatch) (*domain.CollectorCheck, error) {
	f.patches++
	f.lastPatch = patch
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return &domain.CollectorCheck{
		CheckID: "c1", AssetID: "a1", Type: "http", Target: "https://example.test/health",
		IntervalSeconds: 60, Enabled: true, MetricPrefix: "chk", RowVersion: 2,
	}, nil
}

func (f *fakeCollectorChecks) Delete(context.Context, string) error {
	f.deletes++
	return f.deleteErr
}

func TestCollectorChecks_Authorization(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/collector-checks", ""},
		{http.MethodPost, "/v1/admin/collector-checks", `{"asset_id":"a1","target":"https://example.test","interval_seconds":60,"metric_prefix":"chk"}`},
		{http.MethodGet, "/v1/admin/collector-checks/c1", ""},
		{http.MethodPatch, "/v1/admin/collector-checks/c1", `{"row_version":1,"enabled":false}`},
		{http.MethodDelete, "/v1/admin/collector-checks/c1", ""},
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
				spy := &fakeCollectorChecks{}
				srv.SetCollectorChecks(spy)

				req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
				if tok := tc.token(t); tok != "" {
					req.Header.Set("Authorization", "Bearer "+tok)
				}
				rec := httptest.NewRecorder()
				srv.Router().ServeHTTP(rec, req)

				if rec.Code != tc.want {
					t.Errorf("%s %s = %d, want %d", rt.method, rt.path, rec.Code, tc.want)
				}
				if spy.touched() != 0 {
					t.Errorf("%s %s: the store was reached despite refusal", rt.method, rt.path)
				}
			})
		}
	}
}

func TestCollectorChecks_NotConfiguredReturns501(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	// SetCollectorChecks deliberately not called.

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/collector-checks", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
}

// The tenant on the created check came from the resolved connection, not
// from anything the caller could supply — createCollectorCheckRequest has no
// tenant_id field at all, but this pins the invariant the same way
// TestAssets_TenantAdminIsAdmittedAndCannotChooseItsTenant does.
func TestCollectorChecks_CreateUsesResolvedTenantAndDefaultsTypeToHTTP(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeCollectorChecks{}
	srv.SetCollectorChecks(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/collector-checks",
		strings.NewReader(`{"asset_id":"a1","target":"https://example.test/health","interval_seconds":60,"metric_prefix":"chk"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if spy.lastTenant != "t-acme" {
		t.Errorf("tenant = %q, want the caller's resolved tenant t-acme", spy.lastTenant)
	}
	if spy.lastCreated.Type != domain.CollectorCheckTypeHTTP {
		t.Errorf("type = %q, want default %q", spy.lastCreated.Type, domain.CollectorCheckTypeHTTP)
	}
}

// The SSRF egress guard's format half runs at creation time — a scheme other
// than http/https, or a missing host, is refused with 400 before the store is
// ever reached. The IP-level non-public-address guard is proven separately at
// dial time in internal/collector (safehttp.Client), because a hostname's
// resolution can change afterward.
func TestCollectorChecks_CreateRejectsMalformedHTTPTarget(t *testing.T) {
	for _, target := range []string{"file:///etc/passwd", "ftp://example.test/", "not-a-url", "http://"} {
		t.Run(target, func(t *testing.T) {
			srv, _ := newTestServer(true)
			srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
			spy := &fakeCollectorChecks{}
			srv.SetCollectorChecks(spy)

			req := httptest.NewRequest(http.MethodPost, "/v1/admin/collector-checks",
				strings.NewReader(`{"asset_id":"a1","target":"`+target+`","interval_seconds":60,"metric_prefix":"chk"}`))
			req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			if spy.creates != 0 {
				t.Error("a malformed target reached the store")
			}
		})
	}
}

func TestCollectorChecks_CreateRejectsInvalidMetricPrefix(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeCollectorChecks{}
	srv.SetCollectorChecks(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/collector-checks",
		strings.NewReader(`{"asset_id":"a1","target":"https://example.test","interval_seconds":60,"metric_prefix":"Not Valid!"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.creates != 0 {
		t.Error("an invalid metric_prefix reached the store")
	}
}

// asset_id the caller's tenant cannot see maps to 404, not a raw internal
// error — the same shape createAsset's owner-ref check uses.
func TestCollectorChecks_CreateMapsAssetNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeCollectorChecks{createErr: domain.ErrNotFound}
	srv.SetCollectorChecks(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/collector-checks",
		strings.NewReader(`{"asset_id":"cross-tenant","target":"https://example.test","interval_seconds":60,"metric_prefix":"chk"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestCollectorChecks_PatchRequiresRowVersionAndAtLeastOneField(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeCollectorChecks{}
	srv.SetCollectorChecks(spy)
	tok := mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"})

	cases := []string{`{"enabled":false}`, `{"row_version":1}`}
	for _, body := range cases {
		req := httptest.NewRequest(http.MethodPatch, "/v1/admin/collector-checks/c1", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("body %s: status = %d, want 422: %s", body, rec.Code, rec.Body.String())
		}
	}
	if spy.patches != 0 {
		t.Error("an invalid patch reached the store")
	}
}

// Patching target while the check's type is (or becomes) "http" re-validates
// the resulting target's format, fetching the current check to know its
// type/target when only one of the two is in the patch.
func TestCollectorChecks_PatchRevalidatesResultingHTTPTarget(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeCollectorChecks{getCheck: &domain.CollectorCheck{
		CheckID: "c1", AssetID: "a1", Type: "http", Target: "https://example.test", IntervalSeconds: 60,
		Enabled: true, MetricPrefix: "chk", RowVersion: 1,
	}}
	srv.SetCollectorChecks(spy)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/collector-checks/c1",
		strings.NewReader(`{"row_version":1,"target":"ftp://example.test/"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if spy.patches != 0 {
		t.Error("a malformed target reached the store via patch")
	}
}

func TestCollectorChecks_PatchMapsVersionMismatchTo409(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeCollectorChecks{updateErr: domain.ErrVersionMismatch}
	srv.SetCollectorChecks(spy)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/collector-checks/c1",
		strings.NewReader(`{"row_version":1,"enabled":false}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

func TestCollectorChecks_DeleteMapsNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeCollectorChecks{deleteErr: domain.ErrNotFound}
	srv.SetCollectorChecks(spy)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/collector-checks/missing", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestCollectorChecks_GetMapsNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeCollectorChecks{getErr: domain.ErrNotFound}
	srv.SetCollectorChecks(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/collector-checks/missing", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
