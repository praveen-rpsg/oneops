package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

type fakeMaintenanceWindows struct {
	creates, gets, lists, deletes int
	lastTenant                    string
	lastCreated                   *domain.MaintenanceWindow
	createErr                     error
	getErr                        error
	deleteErr                     error
	getWindow                     *domain.MaintenanceWindow
}

func (f *fakeMaintenanceWindows) touched() int {
	return f.creates + f.gets + f.lists + f.deletes
}

func (f *fakeMaintenanceWindows) Create(_ context.Context, w *domain.MaintenanceWindow) (*domain.MaintenanceWindow, error) {
	f.creates++
	f.lastTenant = w.TenantID
	f.lastCreated = w
	if f.createErr != nil {
		return nil, f.createErr
	}
	cp := *w
	cp.CreatedBy = "actor:test"
	cp.RowVersion = 1
	return &cp, nil
}

func (f *fakeMaintenanceWindows) Get(context.Context, string) (*domain.MaintenanceWindow, error) {
	f.gets++
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getWindow != nil {
		cp := *f.getWindow
		return &cp, nil
	}
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	return &domain.MaintenanceWindow{
		WindowID: "w1", AssetID: "a1", StartsAt: start, EndsAt: start.Add(time.Hour),
		CreatedBy: "actor:test", RowVersion: 1,
	}, nil
}

func (f *fakeMaintenanceWindows) List(context.Context, int, string) ([]*domain.MaintenanceWindow, error) {
	f.lists++
	return nil, nil
}

func (f *fakeMaintenanceWindows) Delete(context.Context, string) error {
	f.deletes++
	return f.deleteErr
}

func TestMaintenanceWindows_Authorization(t *testing.T) {
	body := `{"asset_id":"a1","starts_at":"2026-08-01T00:00:00Z","ends_at":"2026-08-01T01:00:00Z"}`
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/maintenance-windows", ""},
		{http.MethodPost, "/v1/admin/maintenance-windows", body},
		{http.MethodGet, "/v1/admin/maintenance-windows/w1", ""},
		{http.MethodDelete, "/v1/admin/maintenance-windows/w1", ""},
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
				spy := &fakeMaintenanceWindows{}
				srv.SetMaintenanceWindows(spy)

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

func TestMaintenanceWindows_NotConfiguredReturns501(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	// SetMaintenanceWindows deliberately not called.

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/maintenance-windows", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
}

// The tenant on the created window came from the resolved connection, not
// from anything the caller could supply — createMaintenanceWindowRequest has
// no tenant_id field at all, the same invariant
// TestCollectorChecks_CreateUsesResolvedTenantAndDefaultsTypeToHTTP pins for
// collector checks.
func TestMaintenanceWindows_CreateUsesResolvedTenant(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeMaintenanceWindows{}
	srv.SetMaintenanceWindows(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/maintenance-windows",
		strings.NewReader(`{"asset_id":"a1","starts_at":"2026-08-01T00:00:00Z","ends_at":"2026-08-01T01:00:00Z","reason":"patching"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if spy.lastTenant != "t-acme" {
		t.Errorf("tenant = %q, want the caller's resolved tenant t-acme", spy.lastTenant)
	}
	if spy.lastCreated.Reason != "patching" {
		t.Errorf("reason = %q, want %q", spy.lastCreated.Reason, "patching")
	}
}

// ends_at <= starts_at is refused before the store is ever reached —
// domain.MaintenanceWindow.Validate's own invariant, surfaced as 422.
func TestMaintenanceWindows_CreateRejectsNonPositiveWindow(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeMaintenanceWindows{}
	srv.SetMaintenanceWindows(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/maintenance-windows",
		strings.NewReader(`{"asset_id":"a1","starts_at":"2026-08-01T01:00:00Z","ends_at":"2026-08-01T00:00:00Z"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.creates != 0 {
		t.Error("an invalid window reached the store")
	}
}

// asset_id the caller's tenant cannot see maps to 404, not a raw internal
// error — the same shape createCollectorCheck's owner-ref check uses.
func TestMaintenanceWindows_CreateMapsAssetNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeMaintenanceWindows{createErr: domain.ErrNotFound}
	srv.SetMaintenanceWindows(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/maintenance-windows",
		strings.NewReader(`{"asset_id":"cross-tenant","starts_at":"2026-08-01T00:00:00Z","ends_at":"2026-08-01T01:00:00Z"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestMaintenanceWindows_GetMapsNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeMaintenanceWindows{getErr: domain.ErrNotFound}
	srv.SetMaintenanceWindows(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/maintenance-windows/missing", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestMaintenanceWindows_DeleteMapsNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeMaintenanceWindows{deleteErr: domain.ErrNotFound}
	srv.SetMaintenanceWindows(spy)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/maintenance-windows/missing", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// suppressed_count/last_suppressed_at round-trip through the DTO — the
// visible, queryable record E3.3a requires (never a silent drop).
func TestMaintenanceWindows_GetSurfacesSuppressionRecord(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	suppressedAt := start.Add(30 * time.Minute)
	spy := &fakeMaintenanceWindows{getWindow: &domain.MaintenanceWindow{
		WindowID: "w1", AssetID: "a1", StartsAt: start, EndsAt: start.Add(time.Hour),
		CreatedBy: "actor:test", RowVersion: 1,
		SuppressedCount: 3, LastSuppressedAt: suppressedAt,
	}}
	srv.SetMaintenanceWindows(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/maintenance-windows/w1", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"suppressed_count":3`) {
		t.Errorf("body = %s, want suppressed_count:3 visible", rec.Body.String())
	}
}
