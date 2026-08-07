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

// SetIOCs wires the indicator-of-compromise repository (E8.2a). Until it is
// called the endpoints report 501 rather than 404, the same convention
// SetSecurityRules/SetAlertRules establish.
func (s *Server) SetIOCs(repo domain.IOCRepository) { s.iocs = repo }

type iocDTO struct {
	IOCID          string    `json:"ioc_id"`
	IndicatorType  string    `json:"indicator_type"`
	IndicatorValue string    `json:"indicator_value"`
	Severity       string    `json:"severity"`
	Enabled        bool      `json:"enabled"`
	Description    string    `json:"description"`
	Source         string    `json:"source"`
	RowVersion     int64     `json:"row_version"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// toIOCDTO deliberately omits tenant_id, the same choice toSecurityRuleDTO
// makes: the caller is already inside exactly one tenant.
func toIOCDTO(i *domain.IOC) iocDTO {
	return iocDTO{
		IOCID: i.IOCID, IndicatorType: string(i.IndicatorType), IndicatorValue: i.IndicatorValue,
		Severity: string(i.Severity), Enabled: i.Enabled, Description: i.Description, Source: i.Source,
		RowVersion: i.RowVersion, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt,
	}
}

// createIOCRequest carries no ioc_id (minted server-side, the same rule
// createSecurityRuleRequest follows). enabled defaults to true;
// description/source default to empty.
type createIOCRequest struct {
	IndicatorType  string  `json:"indicator_type"`
	IndicatorValue string  `json:"indicator_value"`
	Severity       string  `json:"severity"`
	Enabled        *bool   `json:"enabled"`
	Description    *string `json:"description"`
	Source         *string `json:"source"`
}

// patchIOCRequest changes one or more fields under optimistic locking.
// indicator_type/indicator_value are absent — see domain.IOCPatch's doc
// comment: delete and recreate the entry instead of repointing it.
type patchIOCRequest struct {
	RowVersion  int64   `json:"row_version"`
	Severity    *string `json:"severity"`
	Enabled     *bool   `json:"enabled"`
	Description *string `json:"description"`
	Source      *string `json:"source"`
}

func (req patchIOCRequest) anyFieldSet() bool {
	return req.Severity != nil || req.Enabled != nil || req.Description != nil || req.Source != nil
}

// listIOCs returns a page of the caller's watchlist entries.
func (s *Server) listIOCs(w http.ResponseWriter, r *http.Request) {
	if s.iocs == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "ioc administration is not configured")
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
	items, err := s.iocs.List(r.Context(), limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]iocDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toIOCDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createIOC registers a watchlist entry (E8.2a). CONFIG ONLY — no detector
// matches this entry against security_observation yet (E8.2b).
func (s *Server) createIOC(w http.ResponseWriter, r *http.Request) {
	if s.iocs == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "ioc administration is not configured")
		return
	}
	var req createIOCRequest
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

	ioc, err := domain.NewIOC(
		tenant.TenantID, domain.IOCIndicatorType(req.IndicatorType), req.IndicatorValue, domain.IncidentSeverity(req.Severity))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	if req.Enabled != nil {
		ioc.Enabled = *req.Enabled
	}
	if req.Description != nil {
		ioc.Description = *req.Description
	}
	if req.Source != nil {
		ioc.Source = *req.Source
	}

	created, err := s.iocs.Create(r.Context(), ioc)
	if errors.Is(err, domain.ErrConflict) {
		writeProblem(w, r, http.StatusConflict, "conflict",
			"this tenant already has an ioc with the same indicator_type and indicator_value")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toIOCDTO(created))
}

// getIOC returns one watchlist entry by identifier.
func (s *Server) getIOC(w http.ResponseWriter, r *http.Request) {
	if s.iocs == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "ioc administration is not configured")
		return
	}
	ioc, err := s.iocs.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such ioc")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toIOCDTO(ioc))
}

// patchIOC changes one or more fields of a watchlist entry under optimistic
// locking.
func (s *Server) patchIOC(w http.ResponseWriter, r *http.Request) {
	if s.iocs == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "ioc administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchIOCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version", "must be the row_version last read for this ioc"))
		return
	}
	if !req.anyFieldSet() {
		s.mapError(w, r, domain.NewValidationError("body",
			"supply at least one of severity, enabled, description or source"))
		return
	}

	patch := domain.IOCPatch{Enabled: req.Enabled, Description: req.Description, Source: req.Source}
	if req.Severity != nil {
		sev := domain.IncidentSeverity(*req.Severity)
		patch.Severity = &sev
	}

	updated, err := s.iocs.Update(r.Context(), id, req.RowVersion, patch)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such ioc")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "ioc was modified concurrently; re-read and retry")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toIOCDTO(updated))
}

// deleteIOC removes a watchlist entry. Carries no row_version — see
// ADR-HARD-003, which applies here unchanged (mirroring deleteSecurityRule).
func (s *Server) deleteIOC(w http.ResponseWriter, r *http.Request) {
	if s.iocs == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "ioc administration is not configured")
		return
	}
	err := s.iocs.Delete(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such ioc")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
