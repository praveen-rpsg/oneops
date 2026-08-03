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
	"github.com/rpsg/oneops/internal/safehttp"
)

// SetCollectorChecks wires the collector check repository (E2.2a). Until it
// is called the endpoints report 501 rather than 404, the same convention
// SetAssets/SetTelemetry establish.
func (s *Server) SetCollectorChecks(repo domain.CollectorCheckRepository) { s.collectorChecks = repo }

type collectorCheckDTO struct {
	CheckID         string     `json:"check_id"`
	AssetID         string     `json:"asset_id"`
	Type            string     `json:"type"`
	Target          string     `json:"target"`
	IntervalSeconds int        `json:"interval_seconds"`
	Enabled         bool       `json:"enabled"`
	MetricPrefix    string     `json:"metric_prefix"`
	LastRunAt       *time.Time `json:"last_run_at,omitempty"`
	LastStatus      string     `json:"last_status,omitempty"`
	RowVersion      int64      `json:"row_version"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// toCollectorCheckDTO deliberately omits tenant_id, the same choice
// toAssetDTO makes: the caller is already inside exactly one tenant.
func toCollectorCheckDTO(c *domain.CollectorCheck) collectorCheckDTO {
	return collectorCheckDTO{
		CheckID: c.CheckID, AssetID: c.AssetID, Type: c.Type, Target: c.Target,
		IntervalSeconds: c.IntervalSeconds, Enabled: c.Enabled, MetricPrefix: c.MetricPrefix,
		LastRunAt: c.LastRunAt, LastStatus: string(c.LastStatus),
		RowVersion: c.RowVersion, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

// createCollectorCheckRequest carries no check_id (minted server-side, the
// same rule createAssetRequest follows) and no last_run_at/last_status
// (scheduler-owned — see domain.CollectorCheck's doc comment). Type defaults
// to "http" when omitted: it is, today, the only type with an executor.
// Enabled defaults to true when omitted.
type createCollectorCheckRequest struct {
	AssetID         string `json:"asset_id"`
	Type            string `json:"type"`
	Target          string `json:"target"`
	IntervalSeconds int    `json:"interval_seconds"`
	MetricPrefix    string `json:"metric_prefix"`
	Enabled         *bool  `json:"enabled"`
}

// patchCollectorCheckRequest changes one or more fields under optimistic
// locking. asset_id is absent — see domain.CollectorCheckPatch's doc
// comment — and so are last_run_at/last_status, which only the scheduler
// sets.
type patchCollectorCheckRequest struct {
	RowVersion      int64   `json:"row_version"`
	Type            *string `json:"type"`
	Target          *string `json:"target"`
	IntervalSeconds *int    `json:"interval_seconds"`
	Enabled         *bool   `json:"enabled"`
	MetricPrefix    *string `json:"metric_prefix"`
}

func (req patchCollectorCheckRequest) anyFieldSet() bool {
	return req.Type != nil || req.Target != nil || req.IntervalSeconds != nil ||
		req.Enabled != nil || req.MetricPrefix != nil
}

// validateCheckTarget applies the SSRF egress guard's format half at the
// transport boundary — the same split createWebhook draws for
// safehttp.ValidateWebhookURL: a non-http(s) scheme or a missing host is
// refused here, for fast feedback, while the AUTHORITATIVE non-public-address
// guard runs at dial time inside safehttp (ADR-SECURITY-001), because a
// hostname's resolution can change between check creation and the scheduler's
// next poll. Only "http" carries a target format today; a future SNMP/cloud
// check type (E2.2b/c) validates its own target shape here when it lands.
func validateCheckTarget(checkType, target string) error {
	if checkType != domain.CollectorCheckTypeHTTP {
		return nil
	}
	return safehttp.ValidateWebhookURL(target)
}

// listCollectorChecks returns a page of the caller's collector checks.
func (s *Server) listCollectorChecks(w http.ResponseWriter, r *http.Request) {
	if s.collectorChecks == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "collector check administration is not configured")
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
	items, err := s.collectorChecks.List(r.Context(), limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]collectorCheckDTO, 0, len(items))
	for _, c := range items {
		out = append(out, toCollectorCheckDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createCollectorCheck registers a check target.
func (s *Server) createCollectorCheck(w http.ResponseWriter, r *http.Request) {
	if s.collectorChecks == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "collector check administration is not configured")
		return
	}
	var req createCollectorCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	// The tenant comes from the bound connection, never from the caller — the
	// same rule createAsset enforces.
	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	checkType := strings.TrimSpace(req.Type)
	if checkType == "" {
		checkType = domain.CollectorCheckTypeHTTP
	}
	target := strings.TrimSpace(req.Target)
	if err := validateCheckTarget(checkType, target); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad request", err.Error())
		return
	}

	c, err := domain.NewCollectorCheck(tenant.TenantID, req.AssetID, checkType, target, req.IntervalSeconds, req.MetricPrefix)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	if req.Enabled != nil {
		c.Enabled = *req.Enabled
	}

	created, err := s.collectorChecks.Create(r.Context(), c)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "asset_id does not name an asset visible to this tenant")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toCollectorCheckDTO(created))
}

// getCollectorCheck returns one check by identifier.
func (s *Server) getCollectorCheck(w http.ResponseWriter, r *http.Request) {
	if s.collectorChecks == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "collector check administration is not configured")
		return
	}
	c, err := s.collectorChecks.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such collector check")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCollectorCheckDTO(c))
}

// patchCollectorCheck changes one or more fields of a check under optimistic
// locking. When the resulting type is "http", the resulting target is
// re-validated exactly as createCollectorCheck validates it.
func (s *Server) patchCollectorCheck(w http.ResponseWriter, r *http.Request) {
	if s.collectorChecks == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "collector check administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchCollectorCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version", "must be the row_version last read for this check"))
		return
	}
	if !req.anyFieldSet() {
		s.mapError(w, r, domain.NewValidationError("body",
			"supply at least one of type, target, interval_seconds, enabled or metric_prefix"))
		return
	}

	patch := domain.CollectorCheckPatch{IntervalSeconds: req.IntervalSeconds, Enabled: req.Enabled}
	if req.Type != nil {
		t := strings.TrimSpace(*req.Type)
		patch.Type = &t
	}
	if req.Target != nil {
		t := strings.TrimSpace(*req.Target)
		patch.Target = &t
	}
	if req.MetricPrefix != nil {
		t := strings.TrimSpace(*req.MetricPrefix)
		patch.MetricPrefix = &t
	}

	if patch.Type != nil || patch.Target != nil {
		current, err := s.collectorChecks.Get(r.Context(), id)
		if err != nil {
			s.mapError(w, r, err)
			return
		}
		effectiveType := current.Type
		if patch.Type != nil {
			effectiveType = *patch.Type
		}
		effectiveTarget := current.Target
		if patch.Target != nil {
			effectiveTarget = *patch.Target
		}
		if err := validateCheckTarget(effectiveType, effectiveTarget); err != nil {
			writeProblem(w, r, http.StatusBadRequest, "bad request", err.Error())
			return
		}
	}

	updated, err := s.collectorChecks.Update(r.Context(), id, req.RowVersion, patch)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such collector check")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "collector check was modified concurrently; re-read and retry")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCollectorCheckDTO(updated))
}

// deleteCollectorCheck removes a check.
func (s *Server) deleteCollectorCheck(w http.ResponseWriter, r *http.Request) {
	if s.collectorChecks == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "collector check administration is not configured")
		return
	}
	err := s.collectorChecks.Delete(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such collector check")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
