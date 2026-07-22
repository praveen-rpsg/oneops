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

type policyDTO struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Enabled    bool             `json:"enabled"`
	Condition  policy.Condition `json:"condition"`
	Action     policyActionDTO  `json:"action"`
	MaxRetries int              `json:"max_retries"`
	CreatedAt  time.Time        `json:"created_at"`
	UpdatedAt  time.Time        `json:"updated_at"`
}

func toPolicyDTO(p policy.Policy) policyDTO {
	return policyDTO{
		ID: p.ID, Name: p.Name, Enabled: p.Enabled, Condition: p.Condition,
		Action:     policyActionDTO{Type: p.Action.Type, Config: p.Action.Config},
		MaxRetries: p.MaxRetries, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

type createPolicyRequest struct {
	Name       string           `json:"name"`
	Enabled    *bool            `json:"enabled,omitempty"`
	Condition  policy.Condition `json:"condition"`
	Action     policyActionDTO  `json:"action"`
	MaxRetries int              `json:"max_retries,omitempty"`
}

type patchPolicyRequest struct {
	Name       *string           `json:"name,omitempty"`
	Enabled    *bool             `json:"enabled,omitempty"`
	Condition  *policy.Condition `json:"condition,omitempty"`
	Action     *policyActionDTO  `json:"action,omitempty"`
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
		mapError(w, r, err)
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
	if req.Name == "" || req.Action.Type == "" {
		writeProblem(w, r, http.StatusBadRequest, "bad request", "name and action.type are required")
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
		Action: policy.ActionSpec{Type: req.Action.Type, Config: req.Action.Config}, MaxRetries: maxRetries,
	}
	if err := s.policies.Create(r.Context(), p); err != nil {
		mapError(w, r, err)
		return
	}
	created, err := s.policies.Get(r.Context(), p.ID)
	if err != nil {
		mapError(w, r, err)
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
		mapError(w, r, err)
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
	if req.MaxRetries != nil && *req.MaxRetries > 0 {
		cur.MaxRetries = *req.MaxRetries
	}
	if err := s.policies.Update(r.Context(), cur); err != nil {
		mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toPolicyDTO(cur))
}

func (s *Server) deletePolicy(w http.ResponseWriter, r *http.Request) {
	if !s.policiesReady(w, r) {
		return
	}
	if err := s.policies.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		mapError(w, r, err)
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
		mapError(w, r, err)
		return
	}
	xs, err := s.policies.ListByPolicy(r.Context(), id, s.pageLimit(r))
	if err != nil {
		mapError(w, r, err)
		return
	}
	out := make([]policyExecutionDTO, 0, len(xs))
	for _, x := range xs {
		out = append(out, policyExecutionDTO{
			ID: x.ID, PolicyID: x.PolicyID, EventID: x.Event.EventID, Operation: x.Event.Operation,
			CfgID: x.Event.CfgID, Status: string(x.Status), RetryCount: x.RetryCount, Error: x.Error,
			StartedAt: x.StartedAt, EndedAt: x.EndedAt, CreatedAt: x.CreatedAt,
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
		mapError(w, r, err)
		return
	}
	status, terr := s.policyTester(r.Context(), p)
	resp := map[string]any{"status": string(status)}
	if terr != nil {
		resp["error"] = terr.Error()
	}
	writeJSON(w, http.StatusOK, resp)
}
