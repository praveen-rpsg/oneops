package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

type fakeAssets struct {
	creates, gets, lists, patches, deletes, setStatuses, histories int
	relCreates, relDeletes, relFroms, relTos                       int
	lastTenant                                                     string
	createRelErr                                                   error
	createErr                                                      error
	updateErr                                                      error
	setStatusErr                                                   error
	historyErr                                                     error
	lastCreated                                                    *domain.Asset
	lastPatch                                                      domain.AssetPatch
	lastStatus                                                     domain.AssetStatus
	lastListStatus                                                 domain.AssetStatus
	getType                                                        string
	getStatus                                                      domain.AssetStatus
	historyItems                                                   []*domain.AssetChangeEntry
}

func (f *fakeAssets) Create(_ context.Context, a *domain.Asset) (*domain.Asset, error) {
	f.creates++
	f.lastTenant = a.TenantID
	f.lastCreated = a
	if f.createErr != nil {
		return nil, f.createErr
	}
	return a, nil
}
func (f *fakeAssets) Get(context.Context, string) (*domain.Asset, error) {
	f.gets++
	ty := f.getType
	if ty == "" {
		ty = "server"
	}
	status := f.getStatus
	if status == "" {
		status = domain.AssetActive
	}
	return &domain.Asset{AssetID: "a1", Type: ty, Name: "host", Status: status, RowVersion: 1}, nil
}
func (f *fakeAssets) List(_ context.Context, _ int, _ string, status domain.AssetStatus) ([]*domain.Asset, error) {
	f.lists++
	f.lastListStatus = status
	return nil, nil
}
func (f *fakeAssets) Update(_ context.Context, _ string, _ int64, patch domain.AssetPatch) (*domain.Asset, error) {
	f.patches++
	f.lastPatch = patch
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return &domain.Asset{AssetID: "a1", Type: "server", Name: "host", Status: domain.AssetActive, RowVersion: 2}, nil
}
func (f *fakeAssets) SetStatus(_ context.Context, _ string, _ int64, status domain.AssetStatus) (*domain.Asset, error) {
	f.setStatuses++
	f.lastStatus = status
	if f.setStatusErr != nil {
		return nil, f.setStatusErr
	}
	return &domain.Asset{AssetID: "a1", Type: "server", Name: "host", Status: status, RowVersion: 2}, nil
}
func (f *fakeAssets) Delete(context.Context, string) error {
	f.deletes++
	return nil
}
func (f *fakeAssets) History(context.Context, string, int, string) ([]*domain.AssetChangeEntry, error) {
	f.histories++
	if f.historyErr != nil {
		return nil, f.historyErr
	}
	return f.historyItems, nil
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
	return f.creates + f.gets + f.lists + f.patches + f.deletes + f.setStatuses + f.histories +
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
		{http.MethodGet, "/v1/admin/assets/a1/history", ""},
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

// A create request that never mentions classification still gets the
// "unknown" default on both axes — the same default the column carries — so
// a caller that only cares about type/name is never surprised by a required
// field.
func TestAssets_CreateDefaultsClassificationToUnknown(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/assets",
		strings.NewReader(`{"type":"server","name":"host-1"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if spy.lastCreated == nil || spy.lastCreated.Environment != domain.AssetEnvironmentUnknown ||
		spy.lastCreated.Criticality != domain.AssetCriticalityUnknown {
		t.Errorf("defaults not applied: %+v", spy.lastCreated)
	}
}

func TestAssets_CreateRejectsInvalidEnvironmentAndCriticality(t *testing.T) {
	cases := []struct {
		name, body string
	}{
		{"bad environment", `{"type":"server","name":"host-1","environment":"prod"}`},
		{"bad criticality", `{"type":"server","name":"host-1","criticality":"urgent"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := newTestServer(true)
			srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
			spy := &fakeAssets{}
			srv.SetAssets(spy)

			req := httptest.NewRequest(http.MethodPost, "/v1/admin/assets", strings.NewReader(c.body))
			req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
			}
			if spy.creates != 0 {
				t.Error("an invalid enum value reached the store")
			}
		})
	}
}

// The cross-tenant/unknown owner reference defense: the store reports
// ErrNotFound (the same signal it reports for a genuinely nonexistent id,
// per ADR-ASSET-001 §6 extended to ownership), and the handler maps it to
// 404 rather than fabricating a success or a 500.
func TestAssets_CreateMapsOwnerRefNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{createErr: domain.ErrNotFound}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/assets",
		strings.NewReader(`{"type":"server","name":"host-1","owner_team_id":"other-tenants-team"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if spy.creates != 1 {
		t.Errorf("store reached %d time(s), want 1", spy.creates)
	}
}

func TestAssets_PatchMapsOwnerRefNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{updateErr: domain.ErrNotFound}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/assets/a1",
		strings.NewReader(`{"row_version":1,"owner_user_id":"other-tenants-user"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if spy.patches != 1 {
		t.Errorf("store reached %d time(s), want 1", spy.patches)
	}
	if spy.lastPatch.OwnerUserID == nil || *spy.lastPatch.OwnerUserID != "other-tenants-user" {
		t.Errorf("owner_user_id not forwarded to the store: %+v", spy.lastPatch)
	}
}

// A "" owner id clears ownership rather than being rejected or ignored — the
// tri-state AssetPatch.OwnerTeamID/OwnerUserID rule.
func TestAssets_PatchEmptyStringClearsOwner(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/assets/a1",
		strings.NewReader(`{"row_version":1,"owner_team_id":""}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if spy.lastPatch.OwnerTeamID == nil || *spy.lastPatch.OwnerTeamID != "" {
		t.Errorf("clearing owner_team_id was not forwarded as a present, empty value: %+v", spy.lastPatch.OwnerTeamID)
	}
}

// fakeTypedAssetGraph is a domain.GraphTraversal that also implements
// domain.TypedGraphTraversal, mirroring AssetGraphRepo's dual capability.
type fakeTypedAssetGraph struct {
	fakeGraph
	typedCalls int
	typedTypes []string
	typedNodes []domain.TraversalNode
	typedErr   error
}

func (f *fakeTypedAssetGraph) RecursiveDependenciesOfTypes(_ context.Context, _ string, types []string) ([]domain.TraversalNode, error) {
	f.typedCalls++
	f.typedTypes = types
	if f.typedErr != nil {
		return nil, f.typedErr
	}
	return f.typedNodes, nil
}

func TestAssetServiceMap_HappyPathWalksOnlyCompositionEdges(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{getType: "business_service"}
	srv.SetAssets(spy)
	fg := &fakeTypedAssetGraph{typedNodes: []domain.TraversalNode{{CfgID: "db-1", Depth: 1}, {CfgID: "host-1", Depth: 2}}}
	srv.SetAssetGraph(fg)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/assets/svc-1/service-map", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if fg.typedCalls != 1 {
		t.Fatalf("typed traversal called %d time(s), want 1", fg.typedCalls)
	}
	want := map[string]bool{"depends_on": true, "runs_on": true}
	if len(fg.typedTypes) != 2 || !want[fg.typedTypes[0]] || !want[fg.typedTypes[1]] {
		t.Errorf("walked edge types = %v, want exactly depends_on and runs_on", fg.typedTypes)
	}
	var body serviceMapResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.ServiceID != "svc-1" || body.Count != 2 {
		t.Errorf("unexpected response: %+v", body)
	}
}

// A service-map query against an asset that is not a business_service is
// refused before the graph is ever consulted.
func TestAssetServiceMap_RejectsNonBusinessServiceAsset(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{getType: "server"}
	srv.SetAssets(spy)
	fg := &fakeTypedAssetGraph{}
	srv.SetAssetGraph(fg)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/assets/host-1/service-map", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if fg.typedCalls != 0 {
		t.Error("the graph was consulted for a non-business_service asset")
	}
}

func TestAssetServiceMap_NotConfiguredReturns501(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/assets/svc-1/service-map", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
}

// A graph wired with a plain domain.GraphTraversal (no typed capability) is
// a configuration error the endpoint reports as 500, not a silent fallback
// to an unfiltered walk that would misreport a service's composition.
func TestAssetServiceMap_UntypedGraphReturns500(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{getType: "business_service"}
	srv.SetAssets(spy)
	srv.SetAssetGraph(newFakeGraph()) // domain.GraphTraversal only

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/assets/svc-1/service-map", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
}

// A status-only patch goes to SetStatus, never Update — the transport-level
// half of the E1.3 rule that a transition is the only authority for a
// status change.
func TestPatchAsset_StatusOnlyGoesThroughSetStatus(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/assets/a1",
		strings.NewReader(`{"row_version":1,"status":"retired"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if spy.setStatuses != 1 {
		t.Errorf("SetStatus called %d time(s), want 1", spy.setStatuses)
	}
	if spy.patches != 0 {
		t.Error("a status-only patch also reached Update")
	}
	if spy.lastStatus != domain.AssetRetired {
		t.Errorf("status forwarded = %q, want retired", spy.lastStatus)
	}
}

// Combining a status transition with any other field is refused before
// either repository method is called — the same rule patchUser enforces for
// display_name/status.
func TestPatchAsset_RejectsStatusCombinedWithOtherFields(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/assets/a1",
		strings.NewReader(`{"row_version":1,"status":"retired","name":"new-name"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.touched() != 0 {
		t.Error("a combined status+field patch reached the store")
	}
}

// An illegal transition — refused by the domain guard inside the store — must
// surface as 409, not 500 or a silent 200.
func TestPatchAsset_InvalidTransitionMapsToConflict(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{setStatusErr: domain.NewAssetTransitionError(domain.AssetRetired, domain.AssetPlanned)}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/assets/a1",
		strings.NewReader(`{"row_version":1,"status":"planned"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if spy.setStatuses != 1 {
		t.Errorf("SetStatus called %d time(s), want 1", spy.setStatuses)
	}
}

func TestPatchAsset_StatusRejectsUndefinedValue(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/assets/a1",
		strings.NewReader(`{"row_version":1,"status":"decommissioned"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.setStatuses != 0 {
		t.Error("an undefined status reached SetStatus")
	}
}

// listAssets forwards ?status= to the repository, and rejects an undefined
// value before the store is ever reached.
func TestListAssets_ForwardsStatusFilter(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/assets?status=retired", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if spy.lastListStatus != domain.AssetRetired {
		t.Errorf("status filter forwarded = %q, want retired", spy.lastListStatus)
	}
}

func TestListAssets_RejectsUndefinedStatusFilter(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/assets?status=decommissioned", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.lists != 0 {
		t.Error("an undefined status filter reached the store")
	}
}

// createAsset forwards an explicit initial status when it is one of the
// permitted values, and rejects one that is not (maintenance/retired are
// reachable only by transition).
func TestCreateAsset_AcceptsPlannedInitialStatus(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/assets",
		strings.NewReader(`{"type":"server","name":"host-1","status":"planned"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if spy.lastCreated == nil || spy.lastCreated.Status != domain.AssetPlanned {
		t.Errorf("initial status not forwarded: %+v", spy.lastCreated)
	}
}

func TestCreateAsset_RejectsMaintenanceOrRetiredAsInitialStatus(t *testing.T) {
	for _, status := range []string{"maintenance", "retired"} {
		t.Run(status, func(t *testing.T) {
			srv, _ := newTestServer(true)
			srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
			spy := &fakeAssets{}
			srv.SetAssets(spy)

			req := httptest.NewRequest(http.MethodPost, "/v1/admin/assets",
				strings.NewReader(`{"type":"server","name":"host-1","status":"`+status+`"}`))
			req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
			}
			if spy.creates != 0 {
				t.Errorf("%s reached the store as an initial status", status)
			}
		})
	}
}

// getAssetHistory checks the asset exists (404 if not), then serves the
// store's page as-is.
func TestGetAssetHistory_HappyPath(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	oldVal, newVal := "active", "retired"
	spy := &fakeAssets{historyItems: []*domain.AssetChangeEntry{
		{ChangeID: "c1", AssetID: "a1", Kind: domain.AssetChangeTransitioned, Field: "status", OldValue: &oldVal, NewValue: &newVal, Actor: "user-1", RowVersion: 2},
	}}
	srv.SetAssets(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/assets/a1/history", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if spy.gets != 1 || spy.histories != 1 {
		t.Errorf("gets=%d histories=%d, want 1 and 1", spy.gets, spy.histories)
	}
	var body struct {
		Items []assetChangeEntryDTO `json:"items"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].Actor != "user-1" || *body.Items[0].OldValue != "active" || *body.Items[0].NewValue != "retired" {
		t.Errorf("unexpected history payload: %+v", body.Items)
	}
}

func TestGetAssetHistory_NoSuchAssetReturns404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeAssets{}

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/assets/missing/history", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()

	// Swap Get to report not-found for this one request via a wrapper.
	notFound := &fakeAssetsNotFound{fakeAssets: spy}
	srv.SetAssets(notFound)
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if notFound.histories != 0 {
		t.Error("history was queried for an asset that does not exist")
	}
}

// fakeAssetsNotFound wraps fakeAssets and makes Get always report
// domain.ErrNotFound, without disturbing every other fakeAssets test's
// simpler zero-value construction.
type fakeAssetsNotFound struct {
	*fakeAssets
}

func (f *fakeAssetsNotFound) Get(context.Context, string) (*domain.Asset, error) {
	f.gets++
	return nil, domain.ErrNotFound
}
