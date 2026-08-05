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

type fakeOnCallSchedules struct {
	creates, gets, lists, updates, deletes int
	addParticipants, removeParticipants    int
	listParticipants, reorders, onCalls    int

	lastTenant  string
	lastCreated *domain.OnCallSchedule
	lastPatch   domain.OnCallSchedulePatch
	lastAddUser string
	lastReorder []string
	lastAt      time.Time

	createErr, getErr, updateErr, deleteErr error
	addParticipantErr, removeParticipantErr error
	reorderErr, onCallErr                   error

	getSchedule *domain.OnCallSchedule
	onCallRes   *domain.OnCallResolution

	// listSchedules, when non-nil, is returned by List as-is (E7.1's NOC
	// overview test needs to control which schedules — and which Status —
	// come back). nil preserves every existing caller's expectation of an
	// empty list.
	listSchedules []*domain.OnCallSchedule
}

func (f *fakeOnCallSchedules) touched() int {
	return f.creates + f.gets + f.lists + f.updates + f.deletes +
		f.addParticipants + f.removeParticipants + f.listParticipants + f.reorders + f.onCalls
}

func (f *fakeOnCallSchedules) Create(_ context.Context, s *domain.OnCallSchedule) (*domain.OnCallSchedule, error) {
	f.creates++
	f.lastTenant = s.TenantID
	f.lastCreated = s
	if f.createErr != nil {
		return nil, f.createErr
	}
	cp := *s
	cp.RowVersion = 1
	return &cp, nil
}

func (f *fakeOnCallSchedules) Get(context.Context, string) (*domain.OnCallSchedule, error) {
	f.gets++
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getSchedule != nil {
		cp := *f.getSchedule
		return &cp, nil
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &domain.OnCallSchedule{
		ScheduleID: "sch1", Name: "Test Rotation", HandoffIntervalSeconds: 3600,
		RotationStartAt: start, Status: domain.OnCallScheduleActive, RowVersion: 1,
	}, nil
}

func (f *fakeOnCallSchedules) List(context.Context, int, string) ([]*domain.OnCallSchedule, error) {
	f.lists++
	return f.listSchedules, nil
}

func (f *fakeOnCallSchedules) Update(
	_ context.Context, _ string, _ int64, patch domain.OnCallSchedulePatch,
) (*domain.OnCallSchedule, error) {
	f.updates++
	f.lastPatch = patch
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	sch, _ := f.Get(context.Background(), "sch1")
	if patch.Name != nil {
		sch.Name = *patch.Name
	}
	if patch.HandoffIntervalSeconds != nil {
		sch.HandoffIntervalSeconds = *patch.HandoffIntervalSeconds
	}
	if patch.RotationStartAt != nil {
		sch.RotationStartAt = *patch.RotationStartAt
	}
	if patch.Status != nil {
		sch.Status = *patch.Status
	}
	sch.RowVersion++
	return sch, nil
}

func (f *fakeOnCallSchedules) Delete(context.Context, string) error {
	f.deletes++
	return f.deleteErr
}

func (f *fakeOnCallSchedules) AddParticipant(_ context.Context, _, userID string) (*domain.OnCallParticipant, error) {
	f.addParticipants++
	f.lastAddUser = userID
	if f.addParticipantErr != nil {
		return nil, f.addParticipantErr
	}
	return &domain.OnCallParticipant{ParticipantID: "p1", ScheduleID: "sch1", UserID: userID, Position: 0, RowVersion: 1}, nil
}

func (f *fakeOnCallSchedules) RemoveParticipant(context.Context, string, string) error {
	f.removeParticipants++
	return f.removeParticipantErr
}

func (f *fakeOnCallSchedules) ListParticipants(context.Context, string, int, string) ([]*domain.OnCallParticipant, error) {
	f.listParticipants++
	return nil, nil
}

func (f *fakeOnCallSchedules) ReorderParticipants(_ context.Context, _ string, ids []string) ([]*domain.OnCallParticipant, error) {
	f.reorders++
	f.lastReorder = ids
	if f.reorderErr != nil {
		return nil, f.reorderErr
	}
	out := make([]*domain.OnCallParticipant, len(ids))
	for i, id := range ids {
		out[i] = &domain.OnCallParticipant{ParticipantID: id, ScheduleID: "sch1", Position: i, RowVersion: 1}
	}
	return out, nil
}

func (f *fakeOnCallSchedules) OnCall(_ context.Context, scheduleID string, at time.Time) (*domain.OnCallResolution, error) {
	f.onCalls++
	f.lastAt = at
	if f.onCallErr != nil {
		return nil, f.onCallErr
	}
	if f.onCallRes != nil {
		cp := *f.onCallRes
		return &cp, nil
	}
	return &domain.OnCallResolution{ScheduleID: scheduleID, At: at}, nil
}

var _ domain.OnCallScheduleRepository = (*fakeOnCallSchedules)(nil)

func TestOnCallSchedules_Authorization(t *testing.T) {
	body := `{"name":"Rotation","handoff_interval_seconds":3600,"rotation_start_at":"2026-01-01T00:00:00Z"}`
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/on-call-schedules", ""},
		{http.MethodPost, "/v1/admin/on-call-schedules", body},
		{http.MethodGet, "/v1/admin/on-call-schedules/sch1", ""},
		{http.MethodPatch, "/v1/admin/on-call-schedules/sch1", `{"row_version":1,"name":"x"}`},
		{http.MethodDelete, "/v1/admin/on-call-schedules/sch1", ""},
		{http.MethodGet, "/v1/admin/on-call-schedules/sch1/participants", ""},
		{http.MethodPost, "/v1/admin/on-call-schedules/sch1/participants", `{"user_id":"u1"}`},
		{http.MethodDelete, "/v1/admin/on-call-schedules/sch1/participants/p1", ""},
		{http.MethodPost, "/v1/admin/on-call-schedules/sch1/participants/reorder", `{"participant_ids":["p1"]}`},
		{http.MethodGet, "/v1/admin/on-call-schedules/sch1/on-call", ""},
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
				spy := &fakeOnCallSchedules{}
				srv.SetOnCallSchedules(spy)

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

func TestOnCallSchedules_NotConfiguredReturns501(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	// SetOnCallSchedules deliberately not called.

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/on-call-schedules", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
}

// The tenant on the created schedule came from the resolved connection, not
// from anything the caller could supply — createOnCallScheduleRequest has no
// tenant_id field at all.
func TestOnCallSchedules_CreateUsesResolvedTenant(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{}
	srv.SetOnCallSchedules(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/on-call-schedules",
		strings.NewReader(`{"name":"Platform On-Call","handoff_interval_seconds":3600,"rotation_start_at":"2026-01-01T00:00:00Z"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if spy.lastTenant != "t-acme" {
		t.Errorf("tenant = %q, want the caller's resolved tenant t-acme", spy.lastTenant)
	}
	if spy.lastCreated.Name != "Platform On-Call" {
		t.Errorf("name = %q, want %q", spy.lastCreated.Name, "Platform On-Call")
	}
}

// handoff_interval_seconds <= 0 is refused before the store is ever reached
// — domain.OnCallSchedule.Validate's own invariant, surfaced as 422.
func TestOnCallSchedules_CreateRejectsNonPositiveInterval(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{}
	srv.SetOnCallSchedules(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/on-call-schedules",
		strings.NewReader(`{"name":"Bad Rotation","handoff_interval_seconds":0,"rotation_start_at":"2026-01-01T00:00:00Z"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.creates != 0 {
		t.Error("an invalid schedule reached the store")
	}
}

func TestOnCallSchedules_PatchRequiresRowVersion(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{}
	srv.SetOnCallSchedules(spy)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/on-call-schedules/sch1",
		strings.NewReader(`{"name":"New Name"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.updates != 0 {
		t.Error("a patch missing row_version reached the store")
	}
}

func TestOnCallSchedules_PatchMapsVersionMismatchTo409(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{updateErr: domain.ErrVersionMismatch}
	srv.SetOnCallSchedules(spy)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/on-call-schedules/sch1",
		strings.NewReader(`{"row_version":1,"name":"New Name"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// A non-active-member add — cross-tenant or suspended — surfaces as 404,
// never a raw 500.
func TestOnCallSchedules_AddParticipantMapsNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{addParticipantErr: domain.ErrNotFound}
	srv.SetOnCallSchedules(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/on-call-schedules/sch1/participants",
		strings.NewReader(`{"user_id":"not-a-member"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if spy.lastAddUser != "not-a-member" {
		t.Errorf("user_id passed through = %q, want %q", spy.lastAddUser, "not-a-member")
	}
}

func TestOnCallSchedules_AddParticipantMapsConflictTo409(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{addParticipantErr: domain.ErrConflict}
	srv.SetOnCallSchedules(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/on-call-schedules/sch1/participants",
		strings.NewReader(`{"user_id":"already-there"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

func TestOnCallSchedules_RemoveParticipantMapsNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{removeParticipantErr: domain.ErrNotFound}
	srv.SetOnCallSchedules(spy)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/on-call-schedules/sch1/participants/missing", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// A reorder naming the wrong participant set maps the store's
// *domain.ValidationError to 422 via s.mapError.
func TestOnCallSchedules_ReorderMapsValidationErrorTo422(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{reorderErr: domain.NewValidationError("participant_ids", "must name exactly the schedule's current participants")}
	srv.SetOnCallSchedules(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/on-call-schedules/sch1/participants/reorder",
		strings.NewReader(`{"participant_ids":["p1"]}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

func TestOnCallSchedules_ReorderRoundTripsTheNewOrder(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{}
	srv.SetOnCallSchedules(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/on-call-schedules/sch1/participants/reorder",
		strings.NewReader(`{"participant_ids":["p2","p1"]}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(spy.lastReorder) != 2 || spy.lastReorder[0] != "p2" || spy.lastReorder[1] != "p1" {
		t.Errorf("reorder request forwarded %v, want [p2 p1]", spy.lastReorder)
	}
	if !strings.Contains(rec.Body.String(), `"position":0`) || !strings.Contains(rec.Body.String(), `"position":1`) {
		t.Errorf("body = %s, want positions 0 and 1 visible", rec.Body.String())
	}
}

// GET .../on-call defaults `at` to now when omitted, and honours an explicit
// RFC3339 value otherwise.
func TestOnCallSchedules_OnCallDefaultsAtToNow(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{}
	srv.SetOnCallSchedules(spy)

	before := time.Now().UTC()
	req := httptest.NewRequest(http.MethodGet, "/v1/admin/on-call-schedules/sch1/on-call", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	after := time.Now().UTC()

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if spy.onCalls != 1 {
		t.Fatalf("on-call was not called exactly once: %d", spy.onCalls)
	}
	if spy.lastAt.Before(before) || spy.lastAt.After(after) {
		t.Errorf("at = %v, want between %v and %v (defaulted to now)", spy.lastAt, before, after)
	}
}

func TestOnCallSchedules_OnCallHonoursExplicitAt(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{}
	srv.SetOnCallSchedules(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/on-call-schedules/sch1/on-call?at=2026-06-15T12:00:00Z", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"at":"2026-06-15T12:00:00Z"`) {
		t.Errorf("body = %s, want the explicit at echoed back", rec.Body.String())
	}
}

func TestOnCallSchedules_OnCallRejectsMalformedAt(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{}
	srv.SetOnCallSchedules(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/on-call-schedules/sch1/on-call?at=not-a-time", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.onCalls != 0 {
		t.Error("a malformed at reached the store")
	}
}

// N=0: an empty roster renders as a clean 200 with null user_id/display_name
// — never a 404 or a 500.
func TestOnCallSchedules_OnCallWithNoParticipantsRendersNullUser(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{onCallRes: &domain.OnCallResolution{ScheduleID: "sch1", At: time.Now().UTC()}}
	srv.SetOnCallSchedules(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/on-call-schedules/sch1/on-call", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"user_id"`) {
		t.Errorf("body = %s, want user_id omitted when nobody is on call", rec.Body.String())
	}
}

func TestOnCallSchedules_OnCallMapsNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{onCallErr: domain.ErrNotFound}
	srv.SetOnCallSchedules(spy)

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/on-call-schedules/missing/on-call", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestOnCallSchedules_DeleteMapsNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeOnCallSchedules{deleteErr: domain.ErrNotFound}
	srv.SetOnCallSchedules(spy)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/on-call-schedules/missing", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
