package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/policy"
)

// policyRegistry is the admin surface for policy automation. *postgres.PolicyStore
// satisfies it. Test execution is delegated to policyTester (the policy executor).
type policyRegistry interface {
	Create(ctx context.Context, p policy.Policy) error
	Get(ctx context.Context, id string) (policy.Policy, error)
	List(ctx context.Context) ([]policy.Policy, error)
	Update(ctx context.Context, p policy.Policy) error
	Delete(ctx context.Context, id string) error
	ListByPolicy(ctx context.Context, policyID string, limit int) ([]policy.Execution, error)
	// ListByRun returns a composed run's step Executions, ordered by StepIndex
	// (ADR-POLICY-001). Empty (no error) when the run does not exist.
	ListByRun(ctx context.Context, runID string) ([]policy.Execution, error)
}

// SetPolicies wires the policy administration API. tester runs one synthetic
// execution of a policy's action (reusing the policy executor).
func (s *Server) SetPolicies(reg policyRegistry, tester func(ctx context.Context, p policy.Policy) (policy.ExecutionStatus, error)) {
	s.policies = reg
	s.policyTester = tester
}

type policyActionDTO struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
}

// policyGateDTO mirrors policy.GateSpec (ADR-POLICY-001): the Decision that
// must resolve before a composed Sequence advances past the step it guards.
type policyGateDTO struct {
	Type   string          `json:"type"`
	Config json.RawMessage `json:"config,omitempty"`
}

// policyStepDTO mirrors policy.ActionStep: one step of a composed Sequence.
// Gate is nil for an ungated step, exactly as policy.ActionStep.Gate is nil.
type policyStepDTO struct {
	Action policyActionDTO `json:"action"`
	Gate   *policyGateDTO  `json:"gate,omitempty"`
}

// toPolicyStepsDTO/fromPolicyStepsDTO translate policy.Sequence at the HTTP
// boundary only — no domain or storage logic here (ADR-POLICY-001 §W4). An
// empty/absent Sequence round-trips as nil, matching the "Steps is additive,
// not a replacement" backward-compatibility shape.
func toPolicyStepsDTO(seq policy.Sequence) []policyStepDTO {
	if len(seq) == 0 {
		return nil
	}
	out := make([]policyStepDTO, len(seq))
	for i, st := range seq {
		out[i] = policyStepDTO{Action: policyActionDTO{Type: st.Action.Type, Config: st.Action.Config}}
		if st.Gate != nil {
			out[i].Gate = &policyGateDTO{Type: st.Gate.Type, Config: st.Gate.Config}
		}
	}
	return out
}

func fromPolicyStepsDTO(steps []policyStepDTO) policy.Sequence {
	if len(steps) == 0 {
		return nil
	}
	out := make(policy.Sequence, len(steps))
	for i, st := range steps {
		out[i] = policy.ActionStep{Action: policy.ActionSpec{Type: st.Action.Type, Config: st.Action.Config}}
		if st.Gate != nil {
			out[i].Gate = &policy.GateSpec{Type: st.Gate.Type, Config: st.Gate.Config}
		}
	}
	return out
}

type policyDTO struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Enabled    bool             `json:"enabled"`
	Condition  policy.Condition `json:"condition"`
	Action     policyActionDTO  `json:"action"`
	Steps      []policyStepDTO  `json:"steps,omitempty"`
	MaxRetries int              `json:"max_retries"`
	CreatedAt  time.Time        `json:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
}

func toPolicyDTO(p policy.Policy) policyDTO {
	return policyDTO{
		ID: p.ID, Name: p.Name, Enabled: p.Enabled, Condition: p.Condition,
		Action:     policyActionDTO{Type: p.Action.Type, Config: p.Action.Config},
		Steps:      toPolicyStepsDTO(p.Steps),
		MaxRetries: p.MaxRetries, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

type createPolicyRequest struct {
	Name       string           `json:"name"`
	Enabled    *bool            `json:"enabled,omitempty"`
	Condition  policy.Condition `json:"condition"`
	Action     policyActionDTO  `json:"action"`
	Steps      []policyStepDTO  `json:"steps,omitempty"`
	MaxRetries int              `json:"max_retries,omitempty"`
}

type patchPolicyRequest struct {
	Name       *string           `json:"name,omitempty"`
	Enabled    *bool             `json:"enabled,omitempty"`
	Condition  *policy.Condition `json:"condition,omitempty"`
	Action     *policyActionDTO  `json:"action,omitempty"`
	Steps      *[]policyStepDTO  `json:"steps,omitempty"`
	MaxRetries *int              `json:"max_retries,omitempty"`
}

type policyExecutionDTO struct {
	ID         string    `json:"id"`
	PolicyID   string    `json:"policy_id"`
	EventID    string    `json:"event_id"`
	Operation  string    `json:"operation"`
	CfgID      string    `json:"cfg_id"`
	Status     string    `json:"status"`
	RetryCount int       `json:"retry_count"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	EndedAt    time.Time `json:"ended_at,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	// Composed-run coordinates (ADR-POLICY-001): run_id is empty for a
	// single-action execution; when set, it is the id a client passes to
	// GET /admin/policies/{id}/runs/{runId} to inspect the run's progress.
	RunID     string `json:"run_id,omitempty"`
	StepIndex int    `json:"step_index,omitempty"`
	Gate      string `json:"gate,omitempty"`
}

func (s *Server) policiesReady(w http.ResponseWriter, r *http.Request) bool {
	if s.policies == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "policy registry unavailable")
		return false
	}
	return true
}

func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) {
	if !s.policiesReady(w, r) {
		return
	}
	items, err := s.policies.List(r.Context())
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]policyDTO, 0, len(items))
	for _, p := range items {
		out = append(out, toPolicyDTO(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) createPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.policiesReady(w, r) {
		return
	}
	var req createPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "invalid JSON body")
		return
	}
	steps := fromPolicyStepsDTO(req.Steps)
	if req.Name == "" || (req.Action.Type == "" && len(steps) == 0) {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "name and (action.type or steps) are required")
		return
	}
	if err := steps.Validate(); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request", err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	maxRetries := req.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 5
	}
	p := policy.Policy{
		ID: "pol_" + randHex(8), Name: req.Name, Enabled: enabled, Condition: req.Condition,
		Action: policy.ActionSpec{Type: req.Action.Type, Config: req.Action.Config}, Steps: steps,
		MaxRetries: maxRetries,
	}
	if err := s.policies.Create(r.Context(), p); err != nil {
		s.mapError(w, r, err)
		return
	}
	created, err := s.policies.Get(r.Context(), p.ID)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	s.log.Info("policy created", "policy_id", p.ID, "request_id", RequestIDFrom(r.Context()))
	writeJSON(w, http.StatusCreated, toPolicyDTO(created))
}

func (s *Server) patchPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.policiesReady(w, r) {
		return
	}
	cur, err := s.policies.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	var req patchPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "invalid JSON body")
		return
	}
	if req.Name != nil {
		cur.Name = *req.Name
	}
	if req.Enabled != nil {
		cur.Enabled = *req.Enabled
	}
	if req.Condition != nil {
		cur.Condition = *req.Condition
	}
	if req.Action != nil {
		cur.Action = policy.ActionSpec{Type: req.Action.Type, Config: req.Action.Config}
	}
	if req.Steps != nil {
		steps := fromPolicyStepsDTO(*req.Steps)
		if err := steps.Validate(); err != nil {
			writeProblem(w, r, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		cur.Steps = steps
	}
	if req.MaxRetries != nil && *req.MaxRetries > 0 {
		cur.MaxRetries = *req.MaxRetries
	}
	if err := s.policies.Update(r.Context(), cur); err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPolicyDTO(cur))
}

func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request) {
	if !s.policiesReady(w, r) {
		return
	}
	if err := s.policies.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listPolicyExecutions(w http.ResponseWriter, r *http.Request) {
	if !s.policiesReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.policies.Get(r.Context(), id); err != nil {
		s.mapError(w, r, err)
		return
	}
	xs, err := s.policies.ListByPolicy(r.Context(), id, s.pageLimit(r))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]policyExecutionDTO, 0, len(xs))
	for _, x := range xs {
		out = append(out, policyExecutionDTO{
			ID: x.ID, PolicyID: x.PolicyID, EventID: x.Event.EventID, Operation: x.Event.Operation,
			CfgID: x.Event.CfgID, Status: string(x.Status), RetryCount: x.RetryCount, Error: x.Error,
			StartedAt: x.StartedAt, EndedAt: x.EndedAt, CreatedAt: x.CreatedAt,
			RunID: x.RunID, StepIndex: x.StepIndex, Gate: string(x.Gate),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) testPolicy(w http.ResponseWriter, r *http.Request) {
	if !s.policiesReady(w, r) {
		return
	}
	if s.policyTester == nil {
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "policy execution unavailable")
		return
	}
	p, err := s.policies.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	status, terr := s.policyTester(r.Context(), p)
	resp := map[string]any{"status": string(status)}
	if terr != nil {
		resp["error"] = terr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}

// gateStateDTO renders policy.GateState for the wire: GateNone ("") reads as
// "none" rather than an empty string, so a client cannot mistake "no gate
// evaluated yet" for a missing field.
func gateStateDTO(g policy.GateState) string {
	if g == policy.GateNone {
		return "none"
	}
	return string(g)
}

type policyRunStepDTO struct {
	Index       int    `json:"index"`
	Status      string `json:"status"`
	Gate        string `json:"gate"`
	ExecutionID string `json:"execution_id,omitempty"`
	Error       string `json:"error,omitempty"`
}

type policyRunDTO struct {
	RunID        string             `json:"run_id"`
	PolicyID     string             `json:"policy_id"`
	Steps        []policyRunStepDTO `json:"steps"`
	CurrentStep  int                `json:"current_step"`
	Complete     bool               `json:"complete"`
	PausedAtGate bool               `json:"paused_at_gate"`
}

// getPolicyRun is the W4 inspection side of ADR-POLICY-001: a composed run's
// progress is a pure projection (policy.RunProgress) over the run's own
// Execution rows — there is no run entity to fetch, so this reads the
// Policy's Sequence and the run's Executions and renders the same projection
// the executor/reconciler already compute internally.
func (s *Server) getPolicyRun(w http.ResponseWriter, r *http.Request) {
	if !s.policiesReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	runID := chi.URLParam(r, "runId")
	p, err := s.policies.Get(r.Context(), id)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	execs, err := s.policies.ListByRun(r.Context(), runID)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	if len(execs) == 0 {
		writeProblem(w, r, http.StatusNotFound, "not found", "run not found")
		return
	}
	for _, ex := range execs {
		if ex.PolicyID != id {
			writeProblem(w, r, http.StatusNotFound, "not found", "run not found")
			return
		}
	}

	prog := policy.RunProgress{RunID: runID, Seq: p.Steps, Steps: execs}
	byIndex := make(map[int]policy.Execution, len(execs))
	for _, ex := range execs {
		byIndex[ex.StepIndex] = ex
	}
	steps := make([]policyRunStepDTO, len(p.Steps))
	for i := range p.Steps {
		steps[i] = policyRunStepDTO{Index: i, Status: "pending", Gate: "none"}
		if ex, ok := byIndex[i]; ok {
			steps[i].Status = string(ex.Status)
			steps[i].Gate = gateStateDTO(ex.Gate)
			steps[i].ExecutionID = ex.ID
			steps[i].Error = ex.Error
		}
	}
	current := prog.NextStepIndex()
	complete := prog.Complete()
	pausedAtGate := !complete && current < len(steps) &&
		steps[current].Status == string(policy.ExecSucceeded) && steps[current].Gate == gateStateDTO(policy.GatePending)

	writeJSON(w, http.StatusOK, policyRunDTO{
		RunID: runID, PolicyID: id, Steps: steps,
		CurrentStep: current, Complete: complete, PausedAtGate: pausedAtGate,
	})
}
