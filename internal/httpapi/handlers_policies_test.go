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
	runs  map[string][]policy.Execution
}

func newFakePolicyReg() *fakePolicyReg {
	return &fakePolicyReg{m: map[string]policy.Policy{}, execs: map[string][]policy.Execution{}, runs: map[string][]policy.Execution{}}
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
func (f *fakePolicyReg) ListByRun(_ context.Context, runID string) ([]policy.Execution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runs[runID], nil
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

// TestPolicyCompositionRoundTrip defines a 3-step Sequence (one gated) via
// the API and confirms a GET of the same policy (via the list endpoint,
// since there is no single-get route) returns the Steps intact —
// ADR-POLICY-001's "Steps is additive" DTO round-trip.
func TestPolicyCompositionRoundTrip(t *testing.T) {
	h, reg := newPolicyAPI(t, true)

	body := map[string]any{
		"name": "onboard-then-approve-then-provision",
		"steps": []map[string]any{
			{"action": map[string]any{"type": "notification"}},
			{
				"action": map[string]any{"type": "http", "config": map[string]any{"url": "https://example.test"}},
				"gate":   map[string]any{"type": "approval"},
			},
			{"action": map[string]any{"type": "command"}},
		},
	}
	rec := do(h, http.MethodPost, "/v1/admin/policies", body, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var created policyDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(created.Steps) != 3 {
		t.Fatalf("created steps = %+v, want 3", created.Steps)
	}
	if created.Steps[1].Gate == nil || created.Steps[1].Gate.Type != "approval" {
		t.Fatalf("step 1 gate = %+v, want approval", created.Steps[1].Gate)
	}
	if created.Steps[0].Gate != nil || created.Steps[2].Gate != nil {
		t.Fatalf("ungated steps must not gain a gate: %+v", created.Steps)
	}
	if created.Action.Type != "" {
		t.Fatalf("composed-only policy must not synthesize a single Action: %+v", created.Action)
	}

	// The domain object stored behind the registry carries the same Sequence
	// (the store, not just the DTO round-trip, is what a real read reflects).
	stored, err := reg.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(stored.Steps) != 3 || stored.Steps[1].Gate == nil || stored.Steps[1].Gate.Type != "approval" {
		t.Fatalf("stored steps = %+v", stored.Steps)
	}

	// List reflects the same Steps.
	l := do(h, http.MethodGet, "/v1/admin/policies", nil, nil)
	var list struct {
		Items []policyDTO `json:"items"`
	}
	_ = json.Unmarshal(l.Body.Bytes(), &list)
	if len(list.Items) != 1 || len(list.Items[0].Steps) != 3 {
		t.Fatalf("list steps = %+v", list.Items)
	}

	// Patch can replace Steps too.
	patchBody := map[string]any{
		"steps": []map[string]any{
			{"action": map[string]any{"type": "notification"}},
		},
	}
	p := do(h, http.MethodPatch, "/v1/admin/policies/"+created.ID, patchBody, nil)
	var patched policyDTO
	_ = json.Unmarshal(p.Body.Bytes(), &patched)
	if len(patched.Steps) != 1 {
		t.Fatalf("patched steps = %+v, want 1", patched.Steps)
	}
}

// TestPolicyCompositionRejectsBadStep proves a malformed step (missing
// action.type) and a malformed gate (missing gate.type) are both rejected by
// the domain Validate() this handler wires in — a dropped/garbled gate must
// never round-trip as silently accepted.
func TestPolicyCompositionRejectsBadStep(t *testing.T) {
	h, _ := newPolicyAPI(t, true)

	badAction := map[string]any{
		"name":  "bad-action",
		"steps": []map[string]any{{"action": map[string]any{"type": ""}}},
	}
	if rec := do(h, http.MethodPost, "/v1/admin/policies", badAction, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad action step: status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}

	badGate := map[string]any{
		"name": "bad-gate",
		"steps": []map[string]any{
			{"action": map[string]any{"type": "notification"}, "gate": map[string]any{"type": ""}},
		},
	}
	if rec := do(h, http.MethodPost, "/v1/admin/policies", badGate, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad gate: status = %d, want 400, body=%s", rec.Code, rec.Body.String())
	}
}

// TestPolicyRun proves the run-inspection endpoint reflects step statuses,
// gate state and progress, both mid-run (paused at a gate) and complete.
func TestPolicyRun(t *testing.T) {
	h, reg := newPolicyAPI(t, true)

	p := policy.Policy{
		ID: "pol_run", Name: "gated", Enabled: true, MaxRetries: 3,
		Steps: policy.Sequence{
			{Action: policy.ActionSpec{Type: "notification"}},
			{Action: policy.ActionSpec{Type: "http"}, Gate: &policy.GateSpec{Type: policy.GateTypeApproval}},
			{Action: policy.ActionSpec{Type: "command"}},
		},
	}
	if err := reg.Create(context.Background(), p); err != nil {
		t.Fatalf("create: %v", err)
	}
	reg.runs["run_1"] = []policy.Execution{
		{ID: "ex_0", PolicyID: "pol_run", RunID: "run_1", StepIndex: 0, Status: policy.ExecSucceeded},
		{ID: "ex_1", PolicyID: "pol_run", RunID: "run_1", StepIndex: 1, Status: policy.ExecSucceeded, Gate: policy.GatePending},
	}

	rec := do(h, http.MethodGet, "/v1/admin/policies/pol_run/runs/run_1", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var run policyRunDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if run.RunID != "run_1" || run.PolicyID != "pol_run" {
		t.Fatalf("run = %+v", run)
	}
	if len(run.Steps) != 3 {
		t.Fatalf("steps = %+v, want 3", run.Steps)
	}
	if run.Steps[0].Status != "succeeded" || run.Steps[0].Gate != "none" {
		t.Fatalf("step 0 = %+v", run.Steps[0])
	}
	if run.Steps[1].Status != "succeeded" || run.Steps[1].Gate != "pending" {
		t.Fatalf("step 1 = %+v", run.Steps[1])
	}
	if run.Steps[2].Status != "pending" || run.Steps[2].Gate != "none" || run.Steps[2].ExecutionID != "" {
		t.Fatalf("step 2 (never enqueued) = %+v", run.Steps[2])
	}
	if run.CurrentStep != 1 {
		t.Fatalf("current_step = %d, want 1 (paused at the gate)", run.CurrentStep)
	}
	if run.Complete {
		t.Fatal("run must not be complete while paused at a gate")
	}
	if !run.PausedAtGate {
		t.Fatal("run must report paused_at_gate")
	}

	// Resolve the gate and complete the run.
	reg.runs["run_1"] = []policy.Execution{
		reg.runs["run_1"][0],
		{ID: "ex_1", PolicyID: "pol_run", RunID: "run_1", StepIndex: 1, Status: policy.ExecSucceeded, Gate: policy.GatePassed},
		{ID: "ex_2", PolicyID: "pol_run", RunID: "run_1", StepIndex: 2, Status: policy.ExecSucceeded},
	}
	rec = do(h, http.MethodGet, "/v1/admin/policies/pol_run/runs/run_1", nil, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &run)
	if !run.Complete || run.PausedAtGate {
		t.Fatalf("run = %+v, want complete and not paused", run)
	}
	if run.CurrentStep != 3 {
		t.Fatalf("current_step = %d, want 3 (len(steps))", run.CurrentStep)
	}

	// Unknown run: 404.
	if rec := do(h, http.MethodGet, "/v1/admin/policies/pol_run/runs/no-such-run", nil, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
