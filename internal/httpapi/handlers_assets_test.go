package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

type fakeAssets struct {
	creates, gets, lists, patches, deletes   int
	relCreates, relDeletes, relFroms, relTos int
	lastTenant                               string
	createRelErr                             error
}

func (f *fakeAssets) Create(_ context.Context, a *domain.Asset) (*domain.Asset, error) {
	f.creates++
	f.lastTenant = a.TenantID
	return a, nil
}
func (f *fakeAssets) Get(context.Context, string) (*domain.Asset, error) {
	f.gets++
	return &domain.Asset{AssetID: "a1", Type: "server", Name: "host", Status: domain.AssetActive, RowVersion: 1}, nil
}
func (f *fakeAssets) List(context.Context, int, string) ([]*domain.Asset, error) {
	f.lists++
	return nil, nil
}
func (f *fakeAssets) Update(context.Context, string, int64, *string, map[string]any, *domain.AssetStatus) (*domain.Asset, error) {
	f.patches++
	return &domain.Asset{AssetID: "a1", Type: "server", Name: "host", Status: domain.AssetActive, RowVersion: 2}, nil
}
func (f *fakeAssets) Delete(context.Context, string) error {
	f.deletes++
	return nil
}
func (f *fakeAssets) CreateRelationship(_ context.Context, r *domain.AssetRelationship) (*domain.AssetRelationship, error) {
	f.relCreates++
	f.lastTenant = r.TenantID
	if f.createRelErr != nil {
		return nil, f.createRelErr
	}
	return r, nil
}
func (f *fakeAssets) DeleteRelationship(context.Context, string) error {
	f.relDeletes++
	return nil
}
func (f *fakeAssets) RelationshipsFrom(context.Context, string) ([]*domain.AssetRelationship, error) {
	f.relFroms++
	return nil, nil
}
func (f *fakeAssets) RelationshipsTo(context.Context, string) ([]*domain.AssetRelationship, error) {
	f.relTos++
	return nil, nil
}
func (f *fakeAssets) touched() int {
	return f.creates + f.gets + f.lists + f.patches + f.deletes +
		f.relCreates + f.relDeletes + f.relFroms + f.relTos
}

// Asset is tenant-owned, so the authorization tier is tenant administration —
// not platform administration, exactly as Team's is. Every refusal asserts
// the STORE WAS NEVER REACHED, not merely the status code.
func TestAssets_Authorization(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/assets", ""},
		{http.MethodPost, "/v1/admin/assets", `{"type":"server","name":"host"}`},
		{http.MethodGet, "/v1/admin/assets/a1", ""},
		{http.MethodPatch, "/v1/admin/assets/a1", `{"row_version":1,"name":"new"}`},
		{http.MethodDelete, "/v1/admin/assets/a1", ""},
		{http.MethodPost, "/v1/admin/assets/relationships", `{"from_asset_id":"a1","to_asset_id":"a2","type":"depends_on"}`},
		{http.MethodDelete, "/v1/admin/assets/relationships/r1", ""},
		{http.MethodGet, "/v1/admin/assets/a1/relationships", ""},
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
				spy := &fakeAssets{}
				srv.SetAssets(spy)

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

// The tenant administrator succeeds, and the tenant on the created asset came
// from the resolved connection, not from anything the caller supplied.
func TestAssets_TenantAdminIsAdmittedAndCannotChooseItsTenant(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/assets",
		strings.NewReader(`{"type":"server","name":"host-1","tenant_id":"t-victim"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("tenant administrator = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if spy.creates != 1 {
		t.Errorf("store reached %d time(s), want 1", spy.creates)
	}
	if spy.lastTenant != "t-acme" {
		t.Errorf("asset was created in tenant %q, want the caller's resolved tenant %q",
			spy.lastTenant, "t-acme")
	}
}

func TestAssets_CreateRejectsInvalidType(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/assets",
		strings.NewReader(`{"type":"Not Valid!","name":"host-1"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.creates != 0 {
		t.Error("an invalid type reached the store")
	}
}

// A relationship endpoint the store cannot see (or which does not exist) maps
// to 404, not a raw internal error.
func TestAssets_CreateRelationshipMapsNotFound(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{createRelErr: domain.ErrNotFound}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/assets/relationships",
		strings.NewReader(`{"from_asset_id":"a1","to_asset_id":"a2","type":"depends_on"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if spy.relCreates != 1 {
		t.Errorf("store reached %d time(s), want 1", spy.relCreates)
	}
}

func TestAssets_PatchRejectsEmptyBody(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/assets/a1",
		strings.NewReader(`{"row_version":1}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.patches != 0 {
		t.Error("an empty patch reached the store")
	}
}

func TestAssets_NotConfiguredReturns501(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/assets", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
}
