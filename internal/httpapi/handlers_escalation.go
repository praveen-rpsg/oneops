package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/domain"
)

// SetEscalationPolicies wires the escalation policy repository (E5.2b-1).
// Until it is called the endpoints report 501 rather than 404, the same
// convention every other administration surface in this package establishes.
func (s *Server) SetEscalationPolicies(repo domain.EscalationPolicyRepository) {
	s.escalationPolicies = repo
}

// escalationPolicyDTO deliberately omits tenant_id, the same choice every
// other tenant-scoped DTO in this package makes: the caller is already
// inside exactly one tenant.
type escalationPolicyDTO struct {
	PolicyID   string    `json:"policy_id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	RowVersion int64     `json:"row_version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toEscalationPolicyDTO(p *domain.EscalationPolicy) escalationPolicyDTO {
	return escalationPolicyDTO{
		PolicyID: p.PolicyID, Name: p.Name, Status: string(p.Status),
		RowVersion: p.RowVersion, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

type escalationTierDTO struct {
	TierID           string    `json:"tier_id"`
	PolicyID         string    `json:"policy_id"`
	Position         int       `json:"position"`
	OnCallScheduleID string    `json:"on_call_schedule_id"`
	WaitSeconds      int       `json:"wait_seconds"`
	RowVersion       int64     `json:"row_version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func toEscalationTierDTO(t *domain.EscalationTier) escalationTierDTO {
	return escalationTierDTO{
		TierID: t.TierID, PolicyID: t.PolicyID, Position: t.Position,
		OnCallScheduleID: t.OnCallScheduleID, WaitSeconds: t.WaitSeconds,
		RowVersion: t.RowVersion, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

// createEscalationPolicyRequest carries no policy_id: minted server-side.
type createEscalationPolicyRequest struct {
	Name string `json:"name"`
}

// patchEscalationPolicyRequest changes one or more fields under optimistic
// locking, the same shape patchOnCallScheduleRequest uses.
type patchEscalationPolicyRequest struct {
	RowVersion int64   `json:"row_version"`
	Name       *string `json:"name"`
	Status     *string `json:"status"`
}

func (req patchEscalationPolicyRequest) anyFieldSet() bool {
	return req.Name != nil || req.Status != nil
}

// addEscalationTierRequest carries no tier_id: minted server-side, and no
// position: AddTier always appends at the end.
type addEscalationTierRequest struct {
	OnCallScheduleID string `json:"on_call_schedule_id"`
	WaitSeconds      int    `json:"wait_seconds"`
}

// reorderEscalationTiersRequest names the policy's full tier set, in the
// desired new order.
type reorderEscalationTiersRequest struct {
	TierIDs []string `json:"tier_ids"`
}

// listEscalationPolicies returns a page of the caller's escalation policies.
func (s *Server) listEscalationPolicies(w http.ResponseWriter, r *http.Request) {
	if s.escalationPolicies == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "escalation policy administration is not configured")
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeProblem(w, r, http.StatusUnprocessableEntity, "invalid limit", "limit must be an integer")
			return
		}
		limit = n
	}
	items, err := s.escalationPolicies.List(r.Context(), limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]escalationPolicyDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toEscalationPolicyDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createEscalationPolicy declares a new escalation policy.
func (s *Server) createEscalationPolicy(w http.ResponseWriter, r *http.Request) {
	if s.escalationPolicies == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "escalation policy administration is not configured")
		return
	}
	var req createEscalationPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	// The tenant comes from the bound connection, never from the caller —
	// the same rule createOnCallSchedule/createTeam enforce.
	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	p, err := domain.NewEscalationPolicy(tenant.TenantID, req.Name)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	created, err := s.escalationPolicies.Create(r.Context(), p)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toEscalationPolicyDTO(created))
}

// getEscalationPolicy returns one policy by identifier.
func (s *Server) getEscalationPolicy(w http.ResponseWriter, r *http.Request) {
	if s.escalationPolicies == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "escalation policy administration is not configured")
		return
	}
	p, err := s.escalationPolicies.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such escalation policy")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toEscalationPolicyDTO(p))
}

// patchEscalationPolicy changes one or more fields of a policy under
// optimistic locking.
func (s *Server) patchEscalationPolicy(w http.ResponseWriter, r *http.Request) {
	if s.escalationPolicies == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "escalation policy administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchEscalationPolicyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version", "must be the row_version last read for this policy"))
		return
	}
	if !req.anyFieldSet() {
		s.mapError(w, r, domain.NewValidationError("body", "supply at least one of name or status"))
		return
	}

	patch := domain.EscalationPolicyPatch{Name: req.Name}
	if req.Status != nil {
		st := domain.EscalationPolicyStatus(strings.TrimSpace(*req.Status))
		patch.Status = &st
	}

	updated, err := s.escalationPolicies.Update(r.Context(), id, req.RowVersion, patch)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such escalation policy")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "escalation policy was modified concurrently; re-read and retry")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toEscalationPolicyDTO(updated))
}

// deleteEscalationPolicy removes a policy and, via ON DELETE CASCADE, its
// tiers.
func (s *Server) deleteEscalationPolicy(w http.ResponseWriter, r *http.Request) {
	if s.escalationPolicies == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "escalation policy administration is not configured")
		return
	}
	err := s.escalationPolicies.Delete(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such escalation policy")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listEscalationTiers returns a page of a policy's ladder, in escalation
// order.
func (s *Server) listEscalationTiers(w http.ResponseWriter, r *http.Request) {
	if s.escalationPolicies == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "escalation policy administration is not configured")
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeProblem(w, r, http.StatusUnprocessableEntity, "invalid limit", "limit must be an integer")
			return
		}
		limit = n
	}
	items, err := s.escalationPolicies.ListTiers(r.Context(), chi.URLParam(r, "id"), limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]escalationTierDTO, 0, len(items))
	for _, t := range items {
		out = append(out, toEscalationTierDTO(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// addEscalationTier appends a tier to a policy's ladder. on_call_schedule_id
// must already name a schedule belonging to the caller's tenant —
// re-verified server-side (ADR-ASSET-001 §6) — a cross-tenant or
// non-existent on_call_schedule_id is rejected with 404, not a 500.
func (s *Server) addEscalationTier(w http.ResponseWriter, r *http.Request) {
	if s.escalationPolicies == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "escalation policy administration is not configured")
		return
	}
	var req addEscalationTierRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}
	if strings.TrimSpace(req.OnCallScheduleID) == "" {
		s.mapError(w, r, domain.NewValidationError("on_call_schedule_id", "must not be empty"))
		return
	}
	if req.WaitSeconds < domain.MinEscalationWaitSeconds {
		s.mapError(w, r, domain.NewValidationError("wait_seconds", "must be greater than 0"))
		return
	}

	added, err := s.escalationPolicies.AddTier(r.Context(), chi.URLParam(r, "id"), req.OnCallScheduleID, req.WaitSeconds)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found",
			"no such escalation policy, or on_call_schedule_id does not name a schedule of this tenant")
		return
	case errors.Is(err, domain.ErrConflict):
		writeProblem(w, r, http.StatusConflict, "conflict", "tier could not be added")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toEscalationTierDTO(added))
}

// removeEscalationTier removes one tier from a policy's ladder, closing the
// resulting gap in position.
func (s *Server) removeEscalationTier(w http.ResponseWriter, r *http.Request) {
	if s.escalationPolicies == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "escalation policy administration is not configured")
		return
	}
	err := s.escalationPolicies.RemoveTier(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "tierId"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such escalation policy or tier")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reorderEscalationTiers atomically replaces a policy's ladder order.
func (s *Server) reorderEscalationTiers(w http.ResponseWriter, r *http.Request) {
	if s.escalationPolicies == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "escalation policy administration is not configured")
		return
	}
	var req reorderEscalationTiersRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	items, err := s.escalationPolicies.ReorderTiers(r.Context(), chi.URLParam(r, "id"), req.TierIDs)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such escalation policy")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]escalationTierDTO, 0, len(items))
	for _, t := range items {
		out = append(out, toEscalationTierDTO(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
