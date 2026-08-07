package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/domain"
)

// SetSecurityResponseRules wires the security-response-rule repository
// (E8.5). Until it is called the endpoints report 501 rather than 404, the
// same convention SetSecurityRules/SetIOCs establish.
func (s *Server) SetSecurityResponseRules(repo domain.SecurityResponseRuleRepository) {
	s.securityResponseRules = repo
}

type securityResponseRuleDTO struct {
	RuleID       string          `json:"rule_id"`
	Name         string          `json:"name"`
	MinSeverity  string          `json:"min_severity"`
	AssetID      *string         `json:"asset_id,omitempty"`
	ActionType   string          `json:"action_type"`
	ActionConfig json.RawMessage `json:"action_config"`
	Enabled      bool            `json:"enabled"`
	RowVersion   int64           `json:"row_version"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// toSecurityResponseRuleDTO deliberately omits tenant_id, the same choice
// toSecurityRuleDTO/toAlertRuleDTO make: the caller is already inside
// exactly one tenant.
func toSecurityResponseRuleDTO(r *domain.SecurityResponseRule) securityResponseRuleDTO {
	return securityResponseRuleDTO{
		RuleID: r.RuleID, Name: r.Name, MinSeverity: string(r.MinSeverity),
		AssetID: r.AssetID, ActionType: r.ActionType, ActionConfig: r.ActionConfig,
		Enabled: r.Enabled, RowVersion: r.RowVersion, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// createSecurityResponseRuleRequest carries no rule_id (minted server-side,
// the same rule createSecurityRuleRequest follows). action_type MUST be one
// of the SAFE allowlist (http|notification, domain.
// SafeSecurityResponseActionTypes) — "command" (arbitrary execution) or any
// destructive/response action (isolate, block, disable, quarantine) is
// rejected with 422 at domain.NewSecurityResponseRule's own Validate, never
// silently accepted or downgraded. enabled defaults to true.
type createSecurityResponseRuleRequest struct {
	Name         string          `json:"name"`
	MinSeverity  string          `json:"min_severity"`
	AssetID      *string         `json:"asset_id"`
	ActionType   string          `json:"action_type"`
	ActionConfig json.RawMessage `json:"action_config"`
	Enabled      *bool           `json:"enabled"`
}

// patchSecurityResponseRuleRequest changes one or more fields under
// optimistic locking. asset_id/action_type are absent — see
// domain.SecurityResponseRulePatch's own doc comment.
type patchSecurityResponseRuleRequest struct {
	RowVersion   int64           `json:"row_version"`
	Name         *string         `json:"name"`
	MinSeverity  *string         `json:"min_severity"`
	ActionConfig json.RawMessage `json:"action_config"`
	Enabled      *bool           `json:"enabled"`
}

func (req patchSecurityResponseRuleRequest) anyFieldSet() bool {
	return req.Name != nil || req.MinSeverity != nil || req.ActionConfig != nil || req.Enabled != nil
}

// listSecurityResponseRules returns a page of the caller's response rules.
func (s *Server) listSecurityResponseRules(w http.ResponseWriter, r *http.Request) {
	if s.securityResponseRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "security response rule administration is not configured")
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
	items, err := s.securityResponseRules.List(r.Context(), limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]securityResponseRuleDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toSecurityResponseRuleDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createSecurityResponseRule registers a security-response-automation rule
// (E8.5): when a NEW security-sourced incident's severity is at least
// min_severity (and, if asset_id is set, is linked to that one CI), the
// leader-gated SecurityResponder runs action_type exactly once. SAFE ONLY:
// action_type must be "http" or "notification" — any other value (including
// "command", the arbitrary-execution action policy.Registry can otherwise
// run, and any destructive/response verb) is rejected with 422.
func (s *Server) createSecurityResponseRule(w http.ResponseWriter, r *http.Request) {
	if s.securityResponseRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "security response rule administration is not configured")
		return
	}
	var req createSecurityResponseRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	// The tenant comes from the bound connection, never from the caller — the
	// same rule createSecurityRule enforces.
	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	rule, err := domain.NewSecurityResponseRule(
		tenant.TenantID, req.Name, domain.IncidentSeverity(req.MinSeverity), req.AssetID, req.ActionType, req.ActionConfig)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}

	created, err := s.securityResponseRules.Create(r.Context(), rule)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "asset_id does not name an asset visible to this tenant")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSecurityResponseRuleDTO(created))
}

// getSecurityResponseRule returns one rule by identifier.
func (s *Server) getSecurityResponseRule(w http.ResponseWriter, r *http.Request) {
	if s.securityResponseRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "security response rule administration is not configured")
		return
	}
	rule, err := s.securityResponseRules.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such security response rule")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSecurityResponseRuleDTO(rule))
}

// patchSecurityResponseRule changes one or more fields of a rule under
// optimistic locking. action_type cannot be changed here — delete and
// recreate the rule instead (see domain.SecurityResponseRulePatch's own doc
// comment for why: a rule that could be silently repointed from
// "notification" to some future unsafe action via PATCH would defeat the
// create-time SAFE allowlist check).
func (s *Server) patchSecurityResponseRule(w http.ResponseWriter, r *http.Request) {
	if s.securityResponseRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "security response rule administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchSecurityResponseRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version", "must be the row_version last read for this rule"))
		return
	}
	if !req.anyFieldSet() {
		s.mapError(w, r, domain.NewValidationError("body",
			"supply at least one of name, min_severity, action_config or enabled"))
		return
	}

	patch := domain.SecurityResponseRulePatch{Name: req.Name, Enabled: req.Enabled}
	if req.MinSeverity != nil {
		sev := domain.IncidentSeverity(*req.MinSeverity)
		patch.MinSeverity = &sev
	}
	if req.ActionConfig != nil {
		patch.ActionConfig = &req.ActionConfig
	}

	updated, err := s.securityResponseRules.Update(r.Context(), id, req.RowVersion, patch)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such security response rule")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "security response rule was modified concurrently; re-read and retry")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSecurityResponseRuleDTO(updated))
}

// deleteSecurityResponseRule removes a rule. Carries no row_version — see
// ADR-HARD-003, which applies here unchanged (mirroring deleteSecurityRule).
func (s *Server) deleteSecurityResponseRule(w http.ResponseWriter, r *http.Request) {
	if s.securityResponseRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "security response rule administration is not configured")
		return
	}
	err := s.securityResponseRules.Delete(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such security response rule")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
