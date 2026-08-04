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

// SetAlertRules wires the alert rule repository (E3.1). Until it is called
// the endpoints report 501 rather than 404, the same convention
// SetAssets/SetTelemetry/SetCollectorChecks establish.
func (s *Server) SetAlertRules(repo domain.AlertRuleRepository) { s.alertRules = repo }

type alertRuleDTO struct {
	RuleID           string     `json:"rule_id"`
	AssetID          string     `json:"asset_id"`
	Metric           string     `json:"metric"`
	Comparator       string     `json:"comparator"`
	Threshold        float64    `json:"threshold"`
	ForDurationSecs  int        `json:"for_duration_seconds"`
	Severity         string     `json:"severity"`
	Enabled          bool       `json:"enabled"`
	LastState        string     `json:"last_state"`
	LastTransitionAt *time.Time `json:"last_transition_at,omitempty"`
	FlapDwellSecs    int        `json:"flap_dwell_seconds"`
	RowVersion       int64      `json:"row_version"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// toAlertRuleDTO deliberately omits tenant_id, the same choice
// toCollectorCheckDTO makes: the caller is already inside exactly one tenant.
// It also omits pending_state/pending_since (E3.2) — the de-flap dwell's
// in-flight candidate is internal evaluator bookkeeping, not something an
// operator administers or needs to see; flap_dwell_seconds (the config knob)
// is the only part of E3.2 that is part of this contract.
func toAlertRuleDTO(r *domain.AlertRule) alertRuleDTO {
	dto := alertRuleDTO{
		RuleID: r.RuleID, AssetID: r.AssetID, Metric: r.Metric,
		Comparator: string(r.Comparator), Threshold: r.Threshold, ForDurationSecs: r.ForDuration,
		Severity: string(r.Severity), Enabled: r.Enabled, LastState: string(r.LastState),
		FlapDwellSecs: r.FlapDwellSeconds,
		RowVersion:    r.RowVersion, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if !r.LastTransitionAt.IsZero() {
		t := r.LastTransitionAt
		dto.LastTransitionAt = &t
	}
	return dto
}

// createAlertRuleRequest carries no rule_id (minted server-side, the same
// rule createCollectorCheckRequest follows) and no last_state/
// last_transition_at (evaluator-owned — see domain.AlertRule's doc comment).
// severity defaults to "warning" when omitted; flap_dwell_seconds defaults to
// domain.DefaultAlertRuleFlapDwellSeconds when omitted (E3.2).
type createAlertRuleRequest struct {
	AssetID         string  `json:"asset_id"`
	Metric          string  `json:"metric"`
	Comparator      string  `json:"comparator"`
	Threshold       float64 `json:"threshold"`
	ForDurationSecs int     `json:"for_duration_seconds"`
	Severity        string  `json:"severity"`
	Enabled         *bool   `json:"enabled"`
	FlapDwellSecs   *int    `json:"flap_dwell_seconds"`
}

// patchAlertRuleRequest changes one or more fields under optimistic locking.
// asset_id/metric are absent — see domain.AlertRulePatch's doc comment — and
// so are last_state/last_transition_at/pending_state/pending_since, which
// only the evaluator sets (patching ANY field, including flap_dwell_seconds
// itself, clears a rule's in-flight pending candidate — see
// domain.AlertRulePatch's doc comment).
type patchAlertRuleRequest struct {
	RowVersion      int64    `json:"row_version"`
	Comparator      *string  `json:"comparator"`
	Threshold       *float64 `json:"threshold"`
	ForDurationSecs *int     `json:"for_duration_seconds"`
	Severity        *string  `json:"severity"`
	Enabled         *bool    `json:"enabled"`
	FlapDwellSecs   *int     `json:"flap_dwell_seconds"`
}

func (req patchAlertRuleRequest) anyFieldSet() bool {
	return req.Comparator != nil || req.Threshold != nil || req.ForDurationSecs != nil ||
		req.Severity != nil || req.Enabled != nil || req.FlapDwellSecs != nil
}

// listAlertRules returns a page of the caller's alert rules.
func (s *Server) listAlertRules(w http.ResponseWriter, r *http.Request) {
	if s.alertRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "alert rule administration is not configured")
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
	items, err := s.alertRules.List(r.Context(), limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]alertRuleDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toAlertRuleDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createAlertRule registers a threshold rule over one asset's one metric.
func (s *Server) createAlertRule(w http.ResponseWriter, r *http.Request) {
	if s.alertRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "alert rule administration is not configured")
		return
	}
	var req createAlertRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	// The tenant comes from the bound connection, never from the caller — the
	// same rule createCollectorCheck enforces.
	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	severity := domain.AlertSeverity(req.Severity)
	if severity == "" {
		severity = domain.AlertSeverityWarning
	}

	rule, err := domain.NewAlertRule(
		tenant.TenantID, req.AssetID, req.Metric, domain.AlertComparator(req.Comparator),
		req.Threshold, req.ForDurationSecs, severity)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	if req.Enabled != nil {
		rule.Enabled = *req.Enabled
	}
	if req.FlapDwellSecs != nil {
		rule.FlapDwellSeconds = *req.FlapDwellSecs
	}

	created, err := s.alertRules.Create(r.Context(), rule)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "asset_id does not name an asset visible to this tenant")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toAlertRuleDTO(created))
}

// getAlertRule returns one rule by identifier.
func (s *Server) getAlertRule(w http.ResponseWriter, r *http.Request) {
	if s.alertRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "alert rule administration is not configured")
		return
	}
	rule, err := s.alertRules.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such alert rule")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAlertRuleDTO(rule))
}

// patchAlertRule changes one or more fields of a rule under optimistic
// locking.
func (s *Server) patchAlertRule(w http.ResponseWriter, r *http.Request) {
	if s.alertRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "alert rule administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchAlertRuleRequest
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
			"supply at least one of comparator, threshold, for_duration_seconds, severity, enabled or flap_dwell_seconds"))
		return
	}

	patch := domain.AlertRulePatch{
		Threshold: req.Threshold, ForDuration: req.ForDurationSecs, Enabled: req.Enabled,
		FlapDwellSeconds: req.FlapDwellSecs,
	}
	if req.Comparator != nil {
		c := domain.AlertComparator(*req.Comparator)
		patch.Comparator = &c
	}
	if req.Severity != nil {
		sev := domain.AlertSeverity(*req.Severity)
		patch.Severity = &sev
	}

	updated, err := s.alertRules.Update(r.Context(), id, req.RowVersion, patch)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such alert rule")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "alert rule was modified concurrently; re-read and retry")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAlertRuleDTO(updated))
}

// deleteAlertRule removes a rule.
func (s *Server) deleteAlertRule(w http.ResponseWriter, r *http.Request) {
	if s.alertRules == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "alert rule administration is not configured")
		return
	}
	err := s.alertRules.Delete(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such alert rule")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
