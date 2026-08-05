package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

type fakeEscalationPolicies struct {
	creates, gets, lists, updates, deletes int
	addTiers, removeTiers                  int
	listTiers, reorders                    int

	lastTenant   string
	lastCreated  *domain.EscalationPolicy
	lastPatch    domain.EscalationPolicyPatch
	lastAddSched string
	lastAddWait  int
	lastReorder  []string

	createErr, getErr, updateErr, deleteErr error
	addTierErr, removeTierErr               error
	reorderErr                              error

	getPolicy *domain.EscalationPolicy
}

func (f *fakeEscalationPolicies) touched() int {
	return f.creates + f.gets + f.lists + f.updates + f.deletes +
		f.addTiers + f.removeTiers + f.listTiers + f.reorders
}

func (f *fakeEscalationPolicies) Create(_ context.Context, p *domain.EscalationPolicy) (*domain.EscalationPolicy, error) {
	f.creates++
	f.lastTenant = p.TenantID
	f.lastCreated = p
	if f.createErr != nil {
		return nil, f.createErr
	}
	cp := *p
	cp.RowVersion = 1
	return &cp, nil
}

func (f *fakeEscalationPolicies) Get(context.Context, string) (*domain.EscalationPolicy, error) {
	f.gets++
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getPolicy != nil {
		cp := *f.getPolicy
		return &cp, nil
	}
	return &domain.EscalationPolicy{
		PolicyID: "pol1", Name: "Default Policy", Status: domain.EscalationPolicyActive, RowVersion: 1,
	}, nil
}

func (f *fakeEscalationPolicies) List(context.Context, int, string) ([]*domain.EscalationPolicy, error) {
	f.lists++
	return nil, nil
}

func (f *fakeEscalationPolicies) Update(
	_ context.Context, _ string, _ int64, patch domain.EscalationPolicyPatch,
) (*domain.EscalationPolicy, error) {
	f.updates++
	f.lastPatch = patch
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	p, _ := f.Get(context.Background(), "pol1")
	if patch.Name != nil {
		p.Name = *patch.Name
	}
	if patch.Status != nil {
		p.Status = *patch.Status
	}
	p.RowVersion++
	return p, nil
}

func (f *fakeEscalationPolicies) Delete(context.Context, string) error {
	f.deletes++
	return f.deleteErr
}

func (f *fakeEscalationPolicies) AddTier(
	_ context.Context, _, onCallScheduleID string, waitSeconds int,
) (*domain.EscalationTier, error) {
	f.addTiers++
	f.lastAddSched = onCallScheduleID
	f.lastAddWait = waitSeconds
	if f.addTierErr != nil {
		return nil, f.addTierErr
	}
	return &domain.EscalationTier{
		TierID: "t1", PolicyID: "pol1", Position: 0,
		OnCallScheduleID: onCallScheduleID, WaitSeconds: waitSeconds, RowVersion: 1,
	}, nil
}

func (f *fakeEscalationPolicies) RemoveTier(context.Context, string, string) error {
	f.removeTiers++
	return f.removeTierErr
}

func (f *fakeEscalationPolicies) ListTiers(context.Context, string, int, string) ([]*domain.EscalationTier, error) {
	f.listTiers++
	return nil, nil
}

func (f *fakeEscalationPolicies) ReorderTiers(_ context.Context, _ string, ids []string) ([]*domain.EscalationTier, error) {
	f.reorders++
	f.lastReorder = ids
	if f.reorderErr != nil {
		return nil, f.reorderErr
	}
	out := make([]*domain.EscalationTier, len(ids))
	for i, id := range ids {
		out[i] = &domain.EscalationTier{TierID: id, PolicyID: "pol1", Position: i, OnCallScheduleID: "sch1", WaitSeconds: 60, RowVersion: 1}
	}
	return out, nil
}

var _ domain.EscalationPolicyRepository = (*fakeEscalationPolicies)(nil)

func TestEscalationPolicies_Authorization(t *testing.T) {
	body := `{"name":"Default Policy"}`
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/escalation-policies", ""},
		{http.MethodPost, "/v1/admin/escalation-policies", body},
		{http.MethodGet, "/v1/admin/escalation-policies/pol1", ""},
		{http.MethodPatch, "/v1/admin/escalation-policies/pol1", `{"row_version":1,"name":"x"}`},
		{http.MethodDelete, "/v1/admin/escalation-policies/pol1", ""},
		{http.MethodGet, "/v1/admin/escalation-policies/pol1/tiers", ""},
		{http.MethodPost, "/v1/admin/escalation-policies/pol1/tiers", `{"on_call_schedule_id":"sch1","wait_seconds":300}`},
		{http.MethodDelete, "/v1/admin/escalation-policies/pol1/tiers/t1", ""},
		{http.MethodPost, "/v1/admin/escalation-policies/pol1/tiers/reorder", `{"tier_ids":["t1"]}`},
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
				spy := &fakeEscalationPolicies{}
				srv.SetEscalationPolicies(spy)

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

func TestEscalationPolicies_NotConfiguredReturns501(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	// SetEscalationPolicies deliberately not called.

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/escalation-policies", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501: %s", rec.Code, rec.Body.String())
	}
}

// The tenant on the created policy came from the resolved connection, not
// from anything the caller could supply — createEscalationPolicyRequest has
// no tenant_id field at all.
func TestEscalationPolicies_CreateUsesResolvedTenant(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeEscalationPolicies{}
	srv.SetEscalationPolicies(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/escalation-policies",
		strings.NewReader(`{"name":"Default Policy"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if spy.lastTenant != "t-acme" {
		t.Errorf("tenant = %q, want the caller's resolved tenant t-acme", spy.lastTenant)
	}
	if spy.lastCreated.Name != "Default Policy" {
		t.Errorf("name = %q, want %q", spy.lastCreated.Name, "Default Policy")
	}
}

// An empty name is refused before the store is ever reached —
// domain.EscalationPolicy.Validate's own invariant, surfaced as 422.
func TestEscalationPolicies_CreateRejectsEmptyName(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeEscalationPolicies{}
	srv.SetEscalationPolicies(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/escalation-policies", strings.NewReader(`{"name":""}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.creates != 0 {
		t.Error("an invalid policy reached the store")
	}
}

func TestEscalationPolicies_PatchRequiresRowVersion(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeEscalationPolicies{}
	srv.SetEscalationPolicies(spy)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/escalation-policies/pol1",
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

func TestEscalationPolicies_PatchMapsVersionMismatchTo409(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeEscalationPolicies{updateErr: domain.ErrVersionMismatch}
	srv.SetEscalationPolicies(spy)

	req := httptest.NewRequest(http.MethodPatch, "/v1/admin/escalation-policies/pol1",
		strings.NewReader(`{"row_version":1,"name":"New Name"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// wait_seconds <= 0 is refused before the store is ever reached.
func TestEscalationPolicies_AddTierRejectsNonPositiveWaitSeconds(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeEscalationPolicies{}
	srv.SetEscalationPolicies(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/escalation-policies/pol1/tiers",
		strings.NewReader(`{"on_call_schedule_id":"sch1","wait_seconds":0}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if spy.addTiers != 0 {
		t.Error("a non-positive wait_seconds reached the store")
	}
}

// A cross-tenant or non-existent on_call_schedule_id surfaces as 404, never
// a raw 500.
func TestEscalationPolicies_AddTierMapsNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeEscalationPolicies{addTierErr: domain.ErrNotFound}
	srv.SetEscalationPolicies(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/escalation-policies/pol1/tiers",
		strings.NewReader(`{"on_call_schedule_id":"other-tenant-sched","wait_seconds":300}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if spy.lastAddSched != "other-tenant-sched" {
		t.Errorf("on_call_schedule_id passed through = %q, want %q", spy.lastAddSched, "other-tenant-sched")
	}
}

func TestEscalationPolicies_RemoveTierMapsNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeEscalationPolicies{removeTierErr: domain.ErrNotFound}
	srv.SetEscalationPolicies(spy)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/escalation-policies/pol1/tiers/missing", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// A reorder naming the wrong tier set maps the store's *domain.ValidationError
// to 422 via s.mapError.
func TestEscalationPolicies_ReorderMapsValidationErrorTo422(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeEscalationPolicies{reorderErr: domain.NewValidationError("tier_ids", "must name exactly the policy's current tiers")}
	srv.SetEscalationPolicies(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/escalation-policies/pol1/tiers/reorder",
		strings.NewReader(`{"tier_ids":["t1"]}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

func TestEscalationPolicies_ReorderRoundTripsTheNewOrder(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeEscalationPolicies{}
	srv.SetEscalationPolicies(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/escalation-policies/pol1/tiers/reorder",
		strings.NewReader(`{"tier_ids":["t2","t1"]}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(spy.lastReorder) != 2 || spy.lastReorder[0] != "t2" || spy.lastReorder[1] != "t1" {
		t.Errorf("reorder request forwarded %v, want [t2 t1]", spy.lastReorder)
	}
	if !strings.Contains(rec.Body.String(), `"position":0`) || !strings.Contains(rec.Body.String(), `"position":1`) {
		t.Errorf("body = %s, want positions 0 and 1 visible", rec.Body.String())
	}
}

func TestEscalationPolicies_DeleteMapsNotFoundTo404(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	spy := &fakeEscalationPolicies{deleteErr: domain.ErrNotFound}
	srv.SetEscalationPolicies(spy)

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/escalation-policies/missing", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
