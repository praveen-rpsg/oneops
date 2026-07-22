package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/policy"
)

type fakePolicyReg struct {
	mu    sync.Mutex
	m     map[string]policy.Policy
	execs map[string][]policy.Execution
}

func newFakePolicyReg() *fakePolicyReg {
	return &fakePolicyReg{m: map[string]policy.Policy{}, execs: map[string][]policy.Execution{}}
}
func (f *fakePolicyReg) Create(_ context.Context, p policy.Policy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[p.ID] = p
	return nil
}
func (f *fakePolicyReg) Get(_ context.Context, id string) (policy.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.m[id]
	if !ok {
		return policy.Policy{}, domain.ErrNotFound
	}
	return p, nil
}
func (f *fakePolicyReg) List(context.Context) ([]policy.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []policy.Policy
	for _, p := range f.m {
		out = append(out, p)
	}
	return out, nil
}
func (f *fakePolicyReg) Update(_ context.Context, p policy.Policy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[p.ID]; !ok {
		return domain.ErrNotFound
	}
	f.m[p.ID] = p
	return nil
}
func (f *fakePolicyReg) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.m[id]; !ok {
		return domain.ErrNotFound
	}
	delete(f.m, id)
	return nil
}
func (f *fakePolicyReg) ListByPolicy(_ context.Context, id string, _ int) ([]policy.Execution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.execs[id], nil
}

func newPolicyAPI(t *testing.T, wire bool) (http.Handler, *fakePolicyReg) {
	t.Helper()
	reg := newFakePolicyReg()
	cfg := &config.Config{HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200, AuthEnabled: false}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	if wire {
		s.SetPolicies(reg, func(context.Context, policy.Policy) (policy.ExecutionStatus, error) {
			return policy.ExecSucceeded, nil
		})
	}
	return s.Router(), reg
}

func TestPolicyCRUD(t *testing.T) {
	h, reg := newPolicyAPI(t, true)

	body := map[string]any{
		"name":      "notify-on-ratify",
		"condition": map[string]any{"operations": []string{"ratification"}},
		"action":    map[string]any{"type": "notification"},
	}
	rec := do(h, http.MethodPost, "/v1/admin/policies", body, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var created policyDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created.ID == "" || created.Action.Type != "notification" || len(created.Condition.Operations) != 1 {
		t.Fatalf("created = %+v", created)
	}

	// Disable via patch.
	do(h, http.MethodPatch, "/v1/admin/policies/"+created.ID, map[string]any{"enabled": false}, nil)
	if got, _ := reg.Get(context.Background(), created.ID); got.Enabled {
		t.Fatal("policy not disabled")
	}

	// List.
	l := do(h, http.MethodGet, "/v1/admin/policies", nil, nil)
	var list struct {
		Items []policyDTO `json:"items"`
	}
	_ = json.Unmarshal(l.Body.Bytes(), &list)
	if len(list.Items) != 1 {
		t.Fatalf("list = %+v", list.Items)
	}

	// Delete.
	if d := do(h, http.MethodDelete, "/v1/admin/policies/"+created.ID, nil, nil); d.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d", d.Code)
	}
}

func TestPolicyCreateValidation(t *testing.T) {
	h, _ := newPolicyAPI(t, true)
	if rec := do(h, http.MethodPost, "/v1/admin/policies", map[string]any{"name": "x"}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (missing action.type)", rec.Code)
	}
}

func TestPolicyExecutionsAndTest(t *testing.T) {
	h, reg := newPolicyAPI(t, true)
	_ = reg.Create(context.Background(), policy.Policy{ID: "pol_1", Name: "p", Enabled: true, MaxRetries: 3, Action: policy.ActionSpec{Type: "notification"}})
	reg.execs["pol_1"] = []policy.Execution{{ID: "pex_1", PolicyID: "pol_1", Status: policy.ExecSucceeded, Event: policy.Event{Operation: "ratification", EventID: "evt_1"}}}

	ex := do(h, http.MethodGet, "/v1/admin/policies/pol_1/executions", nil, nil)
	var list struct {
		Items []policyExecutionDTO `json:"items"`
	}
	_ = json.Unmarshal(ex.Body.Bytes(), &list)
	if len(list.Items) != 1 || list.Items[0].Status != "succeeded" || list.Items[0].EventID != "evt_1" {
		t.Fatalf("executions = %+v", list.Items)
	}

	tr := do(h, http.MethodPost, "/v1/admin/policies/pol_1/test", nil, nil)
	var testResp map[string]any
	_ = json.Unmarshal(tr.Body.Bytes(), &testResp)
	if testResp["status"] != "succeeded" {
		t.Fatalf("test = %v", testResp)
	}
}

func TestPolicyRBAC(t *testing.T) {
	reg := newFakePolicyReg()
	cfg := &config.Config{HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200, AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	s.SetPolicies(reg, nil)
	h := s.Router()

	editor := map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-editor"})}
	if rec := do(h, http.MethodGet, "/v1/admin/policies", nil, editor); rec.Code != http.StatusForbidden {
		t.Fatalf("editor: status = %d, want 403", rec.Code)
	}
	admin := map[string]string{"Authorization": "Bearer " + mintToken(t, []string{"oneops-admin"})}
	if rec := do(h, http.MethodGet, "/v1/admin/policies", nil, admin); rec.Code != http.StatusOK {
		t.Fatalf("admin: status = %d, want 200", rec.Code)
	}
}

func TestPolicyUnwiredReturns500(t *testing.T) {
	h, _ := newPolicyAPI(t, false)
	if rec := do(h, http.MethodGet, "/v1/admin/policies", nil, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
