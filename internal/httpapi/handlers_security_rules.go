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

// SetSecurityRules wires the security rule repository (E8.1b-1). Until it is
// called the endpoints report 501 rather than 404, the same convention
// SetAlertRules/SetSecurityObservations establish.
func (s *Server) SetSecurityRules(repo domain.SecurityRuleRepository) { s.securityRules = repo }

type securityRuleDTO struct {
	RuleID            string     `json:"rule_id"`
	AssetID           string     `json:"asset_id"`
	ObservationType   string     `json:"observation_type"`
	MinSeverity       string     `json:"min_severity"`
	WindowSeconds     int        `json:"window_seconds"`
	ThresholdCount    int        `json:"threshold_count"`
	IncidentSeverity  string     `json:"incident_severity"`
	Enabled           bool       `json:"enabled"`
	LastState         string     `json:"last_state"`
	LastTransitionAt  *time.Time `json:"last_transition_at,omitempty"`
	CurrentIncidentID *string    `json:"current_incident_id,omitempty"`
	RowVersion        int64      `json:"row_version"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// toSecurityRuleDTO deliberately omits tenant_id, the same choice
// toAlertRuleDTO makes: the caller is already inside exactly one tenant.
// last_state/last_transition_at/current_incident_id are passed straight
// through: they are read-only, detector-owned surfaces (E8.1b-2), the same
// shape toAlertRuleDTO already exposes for the alert path.
func toSecurityRuleDTO(r *domain.SecurityRule) securityRuleDTO {
	dto := securityRuleDTO{
		RuleID: r.RuleID, AssetID: r.AssetID, ObservationType: r.ObservationType,
		MinSeverity: string(r.MinSeverity), WindowSeconds: r.WindowSeconds,
		ThresholdCount: r.ThresholdCount, IncidentSeverity: string(r.IncidentSeverity),
		Enabled: r.Enabled, LastState: string(r.LastState),
		CurrentIncidentID: r.CurrentIncidentID,
		RowVersion:        r.RowVersion, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if !r.LastTransitionAt.IsZero() {
		t := r.LastTransitionAt
		dto.LastTransitionAt = &t
	}
	return dto
}

// createSecurityRuleRequest carries no rule_id (minted server-side, the same
// rule createAlertRuleRequest follows) and no last_state/current_incident_id
// (evaluator-owned — see domain.SecurityRule's doc comment). min_severity
// defaults to domain.DefaultSecurityRuleMinSeverity ("info") when omitted.
// enabled defaults to true.
type createSecurityRuleRequest struct {
	AssetID          string `json:"asset_id"`
	ObservationType  string `json:"observation_type"`
	MinSeverity      string `json:"min_severity"`
	WindowSeconds    int    `json:"window_seconds"`
	ThresholdCount   int    `json:"threshold_count"`
	IncidentSeverity string `json:"incident_severity"`
	Enabled          *bool  `json:"enabled"`
}

// patchSecurityRuleRequest changes one or more fields under optimistic
// locking. asset_id/observation_type are absent — see
// domain.SecurityRulePatch's doc comment — and so are last_state/
// current_incident_id, which only the future detector (E8.1b-2) sets.
type patchSecurityRuleRequest struct {
	RowVersion       int64   `json:"row_version"`
	MinSeverity      *string `json:"min_severity"`
	WindowSeconds    *int    `json:"window_seconds"`
	ThresholdCount   *int    `json:"threshold_count"`
	IncidentSeverity *string `json:"incident_severity"`
	Enabled          *bool   `json:"enabled"`
}

func (req patchSecurityRuleRequest) anyFieldSet() bool {
	return req.MinSeverity != nil || req.WindowSeconds != nil || req.ThresholdCount != nil ||
		req.IncidentSeverity != nil || req.Enabled != nil
}

// listSecurityRules returns a page of the caller's security rules.
func (s *Server) listSecurityRules(w http.ResponseWriter, r *http.Request) {
	if s.securityRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "security rule administration is not configured")
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
	items, err := s.securityRules.List(r.Context(), limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]securityRuleDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toSecurityRuleDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createSecurityRule registers a threshold-over-window detection rule over
// one asset's security observations (E8.1b-1). CONFIG ONLY — no detector
// evaluates this rule yet (E8.1b-2).
func (s *Server) createSecurityRule(w http.ResponseWriter, r *http.Request) {
	if s.securityRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "security rule administration is not configured")
		return
	}
	var req createSecurityRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	// The tenant comes from the bound connection, never from the caller — the
	// same rule createAlertRule enforces.
	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	rule, err := domain.NewSecurityRule(
		tenant.TenantID, req.AssetID, req.ObservationType,
		req.WindowSeconds, req.ThresholdCount, domain.IncidentSeverity(req.IncidentSeverity))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	if req.MinSeverity != "" {
		rule.MinSeverity = domain.ObservationSeverity(req.MinSeverity)
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}

	created, err := s.securityRules.Create(r.Context(), rule)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "asset_id does not name an asset visible to this tenant")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toSecurityRuleDTO(created))
}

// getSecurityRule returns one rule by identifier.
func (s *Server) getSecurityRule(w http.ResponseWriter, r *http.Request) {
	if s.securityRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "security rule administration is not configured")
		return
	}
	rule, err := s.securityRules.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such security rule")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSecurityRuleDTO(rule))
}

// patchSecurityRule changes one or more fields of a rule under optimistic
// locking.
func (s *Server) patchSecurityRule(w http.ResponseWriter, r *http.Request) {
	if s.securityRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "security rule administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchSecurityRuleRequest
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
			"supply at least one of min_severity, window_seconds, threshold_count, incident_severity or enabled"))
		return
	}

	patch := domain.SecurityRulePatch{
		WindowSeconds: req.WindowSeconds, ThresholdCount: req.ThresholdCount, Enabled: req.Enabled,
	}
	if req.MinSeverity != nil {
		sev := domain.ObservationSeverity(*req.MinSeverity)
		patch.MinSeverity = &sev
	}
	if req.IncidentSeverity != nil {
		sev := domain.IncidentSeverity(*req.IncidentSeverity)
		patch.IncidentSeverity = &sev
	}

	updated, err := s.securityRules.Update(r.Context(), id, req.RowVersion, patch)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such security rule")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "security rule was modified concurrently; re-read and retry")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toSecurityRuleDTO(updated))
}

// deleteSecurityRule removes a rule. Carries no row_version — see
// ADR-HARD-003, which applies here unchanged (mirroring deleteAlertRule).
func (s *Server) deleteSecurityRule(w http.ResponseWriter, r *http.Request) {
	if s.securityRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "security rule administration is not configured")
		return
	}
	err := s.securityRules.Delete(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such security rule")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
