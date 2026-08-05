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

type fakeIncidents struct {
	creates, gets, lists, patches, transitions, assigns, timelines int
	createErr, updateErr, setStatusErr, assignErr, timelineErr     error
	lastCreated                                                    *domain.Incident
	lastPatch                                                      domain.IncidentPatch
	lastStatus                                                     domain.IncidentStatus
	lastAssignee                                                   *string
	lastListStatus                                                 domain.IncidentStatus
	timelineItems                                                  []*domain.IncidentEvent
}

func (f *fakeIncidents) Create(_ context.Context, inc *domain.Incident) (*domain.Incident, error) {
	f.creates++
	f.lastCreated = inc
	if f.createErr != nil {
		return nil, f.createErr
	}
	return inc, nil
}
func (f *fakeIncidents) Get(context.Context, string) (*domain.Incident, error) {
	f.gets++
	return &domain.Incident{IncidentID: "i1", Title: "t", Severity: domain.IncidentSeverityHigh, Status: domain.IncidentOpen, RowVersion: 1}, nil
}
func (f *fakeIncidents) List(_ context.Context, _ int, _ string, status domain.IncidentStatus) ([]*domain.Incident, error) {
	f.lists++
	f.lastListStatus = status
	return nil, nil
}
func (f *fakeIncidents) Update(_ context.Context, _ string, _ int64, patch domain.IncidentPatch) (*domain.Incident, error) {
	f.patches++
	f.lastPatch = patch
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	return &domain.Incident{IncidentID: "i1", Title: "t", Severity: domain.IncidentSeverityHigh, Status: domain.IncidentOpen, RowVersion: 2}, nil
}
func (f *fakeIncidents) SetStatus(_ context.Context, _ string, _ int64, status domain.IncidentStatus) (*domain.Incident, error) {
	f.transitions++
	f.lastStatus = status
	if f.setStatusErr != nil {
		return nil, f.setStatusErr
	}
	return &domain.Incident{IncidentID: "i1", Title: "t", Severity: domain.IncidentSeverityHigh, Status: status, RowVersion: 2}, nil
}
func (f *fakeIncidents) Assign(_ context.Context, _ string, _ int64, assigneeUserID *string) (*domain.Incident, error) {
	f.assigns++
	f.lastAssignee = assigneeUserID
	if f.assignErr != nil {
		return nil, f.assignErr
	}
	return &domain.Incident{IncidentID: "i1", Title: "t", Severity: domain.IncidentSeverityHigh, Status: domain.IncidentOpen, AssigneeUserID: assigneeUserID, RowVersion: 2}, nil
}
func (f *fakeIncidents) Timeline(context.Context, string, int, string) ([]*domain.IncidentEvent, error) {
	f.timelines++
	if f.timelineErr != nil {
		return nil, f.timelineErr
	}
	return f.timelineItems, nil
}
func (f *fakeIncidents) touched() int {
	return f.creates + f.gets + f.lists + f.patches + f.transitions + f.assigns + f.timelines
}

// Incident is tenant-owned, so the authorization tier is tenant
// administration — not platform administration. Every refusal asserts the
// STORE WAS NEVER REACHED, not merely the status code — mirrors
// TestAssets_Authorization.
func TestIncidents_Authorization(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/incidents", ""},
		{http.MethodPost, "/v1/admin/incidents", `{"title":"x","severity":"high"}`},
		{http.MethodGet, "/v1/admin/incidents/i1", ""},
		{http.MethodPatch, "/v1/admin/incidents/i1", `{"row_version":1,"title":"y"}`},
		{http.MethodPost, "/v1/admin/incidents/i1/transition", `{"row_version":1,"status":"acknowledged"}`},
		{http.MethodPost, "/v1/admin/incidents/i1/assign", `{"row_version":1,"assignee_user_id":"u1"}`},
		{http.MethodGet, "/v1/admin/incidents/i1/timeline", ""},
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
				spy := &fakeIncidents{}
				srv.SetIncidents(spy)

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

// The tenant administrator succeeds, and the tenant on the created incident
// came from the resolved connection, not from anything the caller supplied.
func TestIncidents_TenantAdminIsAdmittedAndCannotChooseItsTenant(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeIncidents{}
	srv.SetIncidents(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/incidents",
		strings.NewReader(`{"title":"db down","severity":"critical","tenant_id":"t-victim"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("tenant administrator = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if spy.creates != 1 {
		t.Errorf("store reached %d time(s), want 1", spy.creates)
	}
	if spy.lastCreated.TenantID != "t-acme" {
		t.Errorf("incident was created in tenant %q, want the caller's resolved tenant %q",
			spy.lastCreated.TenantID, "t-acme")
	}
}

func TestIncidents_CreateRejectsInvalidSeverity(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeIncidents{}
	srv.SetIncidents(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/incidents",
		strings.NewReader(`{"title":"x","severity":"catastrophic"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.creates != 0 {
		t.Error("an invalid severity reached the store")
	}
}

// The cross-tenant/unknown reference defense: the store reports ErrNotFound
// (the same signal for a genuinely nonexistent id, per ADR-ASSET-001 §6
// extended to the assignee), and the handler maps it to 404 rather than a 500.
func TestIncidents_CreateMapsAssetOrAssigneeNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeIncidents{createErr: domain.ErrNotFound}
	srv.SetIncidents(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/incidents",
		strings.NewReader(`{"title":"x","severity":"high","assignee_user_id":"no-such-active-member"}`))
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

func TestIncidents_PatchRejectsEmptyBody(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeIncidents{}
	srv.SetIncidents(spy)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/incidents/i1",
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

// A refused lifecycle move (ErrInvalidTransition) maps to 409, matching
// asset's own transition-conflict shape.
func TestIncidents_TransitionMapsIllegalMoveTo409(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeIncidents{setStatusErr: domain.NewIncidentTransitionError(domain.IncidentOpen, domain.IncidentClosed)}
	srv.SetIncidents(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/incidents/i1/transition",
		strings.NewReader(`{"row_version":1,"status":"closed"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if spy.transitions != 1 {
		t.Errorf("store reached %d time(s), want 1", spy.transitions)
	}
}

func TestIncidents_TransitionRejectsUnknownStatus(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeIncidents{}
	srv.SetIncidents(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/incidents/i1/transition",
		strings.NewReader(`{"row_version":1,"status":"vanished"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.transitions != 0 {
		t.Error("an unknown status reached the store")
	}
}

func TestIncidents_AssignClearsWithNullAssignee(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeIncidents{}
	srv.SetIncidents(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/incidents/i1/assign",
		strings.NewReader(`{"row_version":1,"assignee_user_id":null}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if spy.lastAssignee != nil {
		t.Errorf("lastAssignee = %v, want nil (cleared)", spy.lastAssignee)
	}
}

// The cross-tenant assignee defense on the dedicated assign endpoint.
func TestIncidents_AssignMapsNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeIncidents{assignErr: domain.ErrNotFound}
	srv.SetIncidents(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/incidents/i1/assign",
		strings.NewReader(`{"row_version":1,"assignee_user_id":"outsider"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if spy.assigns != 1 {
		t.Errorf("store reached %d time(s), want 1", spy.assigns)
	}
}

func TestIncidents_NotConfiguredReturns501(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/incidents", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
}

func TestIncidents_ListRejectsInvalidStatus(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeIncidents{}
	srv.SetIncidents(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/incidents?status=bogus", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.lists != 0 {
		t.Error("an invalid status filter reached the store")
	}
}

// TestToIncidentDTO_ProjectsGroupingReadOnly is E4.2's DTO projection: a root
// or standalone incident (RootIncidentID nil) reports root_incident_id absent
// (omitempty) and is_root true; a grouped incident reports root_incident_id
// present and is_root false. There is no field anywhere on
// createIncidentRequest/incidentPatchRequest for this — the only writer is
// internal/grouping's reconciler, never this handler.
func TestToIncidentDTO_ProjectsGroupingReadOnly(t *testing.T) {
	root := &domain.Incident{
		IncidentID: "i-root", Title: "t", Severity: domain.IncidentSeverityHigh,
		Status: domain.IncidentOpen, Source: domain.IncidentSourceAlert,
	}
	dto := toIncidentDTO(root)
	if dto.RootIncidentID != nil {
		t.Errorf("root incident RootIncidentID = %v, want nil", *dto.RootIncidentID)
	}
	if !dto.IsRoot {
		t.Error("root incident IsRoot = false, want true")
	}
	body, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(body), "root_incident_id") {
		t.Errorf("root_incident_id present in JSON for a root incident (omitempty should drop it): %s", body)
	}

	grouped := &domain.Incident{
		IncidentID: "i-child", Title: "t", Severity: domain.IncidentSeverityHigh,
		Status: domain.IncidentOpen, Source: domain.IncidentSourceAlert,
		RootIncidentID: strPtr("i-root"),
	}
	dto2 := toIncidentDTO(grouped)
	if dto2.RootIncidentID == nil || *dto2.RootIncidentID != "i-root" {
		t.Errorf("grouped incident RootIncidentID = %v, want i-root", dto2.RootIncidentID)
	}
	if dto2.IsRoot {
		t.Error("grouped incident IsRoot = true, want false")
	}
}

func strPtr(s string) *string { return &s }
