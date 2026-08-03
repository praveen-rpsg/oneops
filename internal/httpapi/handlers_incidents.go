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

// SetIncidents wires the ITSM incident repository. Until it is called the
// endpoints report 501 rather than 404, so a deployment that has not enabled
// incident management says so instead of looking like a routing mistake —
// mirrors SetAssets.
func (s *Server) SetIncidents(repo domain.IncidentRepository) { s.incidents = repo }

type incidentDTO struct {
	IncidentID     string     `json:"incident_id"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	Severity       string     `json:"severity"`
	Status         string     `json:"status"`
	AssetID        *string    `json:"asset_id,omitempty"`
	AssigneeUserID *string    `json:"assignee_user_id,omitempty"`
	RowVersion     int64      `json:"row_version"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	ClosedAt       *time.Time `json:"closed_at,omitempty"`
}

// toIncidentDTO deliberately omits tenant_id — the same choice toAssetDTO/
// toTeamDTO make: the caller is already inside exactly one tenant.
func toIncidentDTO(inc *domain.Incident) incidentDTO {
	return incidentDTO{
		IncidentID: inc.IncidentID, Title: inc.Title, Description: inc.Description,
		Severity: string(inc.Severity), Status: string(inc.Status),
		AssetID: inc.AssetID, AssigneeUserID: inc.AssigneeUserID,
		RowVersion: inc.RowVersion, CreatedAt: inc.CreatedAt, UpdatedAt: inc.UpdatedAt,
		AcknowledgedAt: inc.AcknowledgedAt, ResolvedAt: inc.ResolvedAt, ClosedAt: inc.ClosedAt,
	}
}

// incidentEventDTO is one row of an incident's append-only timeline (E5.1).
// OldValue/NewValue are omitted when nil rather than rendered as JSON null —
// IncidentEventCreated carries neither, and a cleared assignment is
// represented by an absent new_value, not a null one.
type incidentEventDTO struct {
	EventID    string    `json:"event_id"`
	IncidentID string    `json:"incident_id"`
	Kind       string    `json:"kind"`
	Field      string    `json:"field,omitempty"`
	OldValue   *string   `json:"old_value,omitempty"`
	NewValue   *string   `json:"new_value,omitempty"`
	Actor      string    `json:"actor"`
	RowVersion int64     `json:"row_version"`
	OccurredAt time.Time `json:"occurred_at"`
}

func toIncidentEventDTO(e *domain.IncidentEvent) incidentEventDTO {
	return incidentEventDTO{
		EventID: e.EventID, IncidentID: e.IncidentID, Kind: string(e.Kind), Field: e.Field,
		OldValue: e.OldValue, NewValue: e.NewValue, Actor: e.Actor,
		RowVersion: e.RowVersion, OccurredAt: e.OccurredAt,
	}
}

// createIncidentRequest carries no incident_id: the identifier is minted
// server-side, the same rule createAssetRequest/createTeamRequest follow.
// asset_id/assignee_user_id are optional; when set, s.incidents.Create
// re-verifies each against the caller's tenant before the incident is
// written (asset_id per ADR-ASSET-001 §6; assignee_user_id via an active
// membership, per E1.2's owner-verification pattern extended to Incident).
// Status is never accepted here — a new incident is always IncidentOpen.
type createIncidentRequest struct {
	Title          string  `json:"title"`
	Description    string  `json:"description"`
	Severity       string  `json:"severity"`
	AssetID        *string `json:"asset_id"`
	AssigneeUserID *string `json:"assignee_user_id"`
}

// patchIncidentRequest changes one or more of title/description/severity/
// asset_id. status and assignee_user_id are deliberately absent: SetStatus
// (POST .../transition) and Assign (POST .../assign) are their single
// authorities, exactly as patchAssetRequest excludes status.
type patchIncidentRequest struct {
	RowVersion  int64   `json:"row_version"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Severity    *string `json:"severity"`
	AssetID     *string `json:"asset_id"`
}

func (req patchIncidentRequest) anyFieldSet() bool {
	return req.Title != nil || req.Description != nil || req.Severity != nil || req.AssetID != nil
}

type transitionIncidentRequest struct {
	RowVersion int64  `json:"row_version"`
	Status     string `json:"status"`
}

// assignIncidentRequest's AssigneeUserID is a plain *string, not tri-state
// like patchAssetRequest's owner fields: this endpoint's only job is
// assignment, so an absent field is simply invalid input (row_version alone
// is not a valid assign request) rather than "leave unchanged" — a caller
// clears an assignment by sending assignee_user_id: null explicitly.
type assignIncidentRequest struct {
	RowVersion     int64   `json:"row_version"`
	AssigneeUserID *string `json:"assignee_user_id"`
}

// listIncidents returns a page of the caller's incidents. Unlike listAssets,
// there is no default status exclusion (domain.IncidentRepository.List's doc
// comment): a closed incident remains relevant operational history.
func (s *Server) listIncidents(w http.ResponseWriter, r *http.Request) {
	if s.incidents == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "incident management is not configured")
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
	status := domain.IncidentStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && !status.Valid() {
		s.mapError(w, r, domain.NewValidationError("status",
			"must be one of: open, acknowledged, investigating, resolved, closed, reopened"))
		return
	}

	items, err := s.incidents.List(r.Context(), limit, r.URL.Query().Get("after"), status)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]incidentDTO, 0, len(items))
	for _, inc := range items {
		out = append(out, toIncidentDTO(inc))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createIncident files a new Incident, always IncidentOpen.
func (s *Server) createIncident(w http.ResponseWriter, r *http.Request) {
	if s.incidents == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "incident management is not configured")
		return
	}
	var req createIncidentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	// The tenant comes from the bound connection, never from the caller — the
	// same rule createAsset/createTeam enforce.
	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	inc, err := domain.NewIncident(tenant.TenantID, req.Title, req.Description,
		domain.IncidentSeverity(strings.TrimSpace(req.Severity)), req.AssetID, req.AssigneeUserID)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	created, err := s.incidents.Create(r.Context(), inc)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found",
			"asset_id does not name a Configuration Item visible to this tenant, or assignee_user_id does not name a user with an active membership in this tenant")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toIncidentDTO(created))
}

// getIncident returns one incident by identifier.
func (s *Server) getIncident(w http.ResponseWriter, r *http.Request) {
	if s.incidents == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "incident management is not configured")
		return
	}
	inc, err := s.incidents.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such incident")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toIncidentDTO(inc))
}

// patchIncident changes title/description/severity/asset_id. status and
// assignee_user_id go through their own dedicated endpoints (see
// patchIncidentRequest's doc comment).
func (s *Server) patchIncident(w http.ResponseWriter, r *http.Request) {
	if s.incidents == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "incident management is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchIncidentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version", "must be the row_version last read for this incident"))
		return
	}
	if !req.anyFieldSet() {
		s.mapError(w, r, domain.NewValidationError("body",
			"supply at least one of title, description, severity or asset_id"))
		return
	}

	patch := domain.IncidentPatch{Title: req.Title, Description: req.Description, AssetID: req.AssetID}
	if req.Severity != nil {
		sev := domain.IncidentSeverity(strings.TrimSpace(*req.Severity))
		patch.Severity = &sev
	}

	updated, err := s.incidents.Update(r.Context(), id, req.RowVersion, patch)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found",
			"no such incident, or asset_id does not name a Configuration Item visible to this tenant")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "incident was modified concurrently; re-read and retry")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toIncidentDTO(updated))
}

// transitionIncident performs a lifecycle move — the single authority for
// status changes (domain.IncidentStatus.CanTransitionTo).
func (s *Server) transitionIncident(w http.ResponseWriter, r *http.Request) {
	if s.incidents == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "incident management is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req transitionIncidentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version", "must be the row_version last read for this incident"))
		return
	}
	status := domain.IncidentStatus(strings.TrimSpace(req.Status))
	if !status.Valid() {
		s.mapError(w, r, domain.NewValidationError("status",
			"must be one of: open, acknowledged, investigating, resolved, closed, reopened"))
		return
	}

	updated, err := s.incidents.SetStatus(r.Context(), id, req.RowVersion, status)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such incident")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "incident was modified concurrently; re-read and retry")
		return
	case errors.Is(err, domain.ErrInvalidTransition):
		// The target state is well-formed; it is the move that is refused —
		// a conflict with the record's current state, mirrors mapError's own
		// ErrInvalidTransition branch (errors.go), applied explicitly here so
		// the 404/409 ordering above stays readable as one switch.
		writeProblem(w, r, http.StatusConflict, "conflict", err.Error())
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toIncidentDTO(updated))
}

// assignIncident changes the incident's assignee — the single authority for
// that field, tenant-verified via an active membership.
func (s *Server) assignIncident(w http.ResponseWriter, r *http.Request) {
	if s.incidents == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "incident management is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req assignIncidentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version", "must be the row_version last read for this incident"))
		return
	}

	updated, err := s.incidents.Assign(r.Context(), id, req.RowVersion, req.AssigneeUserID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found",
			"no such incident, or assignee_user_id does not name a user with an active membership in this tenant")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "incident was modified concurrently; re-read and retry")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toIncidentDTO(updated))
}

// getIncidentTimeline serves GET /v1/admin/incidents/{id}/timeline: the
// append-only case history of an Incident (E5.1) — created, status
// transitions, assignment changes, each with actor and old->new, in
// chronological order.
func (s *Server) getIncidentTimeline(w http.ResponseWriter, r *http.Request) {
	if s.incidents == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "incident management is not configured")
		return
	}
	id := chi.URLParam(r, "id")
	if _, err := s.incidents.Get(r.Context(), id); err != nil {
		s.mapError(w, r, err)
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

	items, err := s.incidents.Timeline(r.Context(), id, limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]incidentEventDTO, 0, len(items))
	for _, e := range items {
		out = append(out, toIncidentEventDTO(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
