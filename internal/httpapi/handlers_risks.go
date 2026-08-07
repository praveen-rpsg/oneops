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

// SetRisks wires the risk-register repository (E8.4a). Until it is called
// the endpoints report 501 rather than 404, the same convention
// SetVulnFindings/SetIncidents establish.
func (s *Server) SetRisks(repo domain.RiskRepository) { s.risks = repo }

type riskDTO struct {
	RiskID      string    `json:"risk_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	Likelihood  string    `json:"likelihood"`
	Impact      string    `json:"impact"`
	Status      string    `json:"status"`
	Treatment   *string   `json:"treatment,omitempty"`
	AssetID     *string   `json:"asset_id,omitempty"`
	RowVersion  int64     `json:"row_version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// toRiskDTO deliberately omits tenant_id, the same choice
// toVulnFindingDTO/toIncidentDTO make: the caller is already inside exactly
// one tenant.
func toRiskDTO(r *domain.Risk) riskDTO {
	var treatment *string
	if r.Treatment != nil {
		v := string(*r.Treatment)
		treatment = &v
	}
	return riskDTO{
		RiskID: r.RiskID, Title: r.Title, Description: r.Description, Category: r.Category,
		Likelihood: string(r.Likelihood), Impact: string(r.Impact), Status: string(r.Status),
		Treatment: treatment, AssetID: r.AssetID,
		RowVersion: r.RowVersion, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// createRiskRequest carries no risk_id: the identifier is minted
// server-side, the same rule every other create route in this schema
// follows. Treatment/AssetID are optional; when AssetID is set,
// s.risks.Create re-verifies it against the caller's tenant before the risk
// is written (ADR-ASSET-001 §6, extended here, mirroring
// createIncidentRequest).
type createRiskRequest struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Category    string  `json:"category"`
	Likelihood  string  `json:"likelihood"`
	Impact      string  `json:"impact"`
	Treatment   *string `json:"treatment"`
	AssetID     *string `json:"asset_id"`
}

// patchRiskRequest changes one or more non-lifecycle fields in a single
// call, OR performs exactly one lifecycle transition — never both in the
// same request (E8.4a), mirroring patchAssetRequest's identical split: the
// two go through different repository methods on purpose, so a transition
// is always checked by RiskStatus.CanTransitionTo alone, and a call that
// tried to do both would consume two row versions and leave the caller
// unable to say which half failed.
//
// Category/Treatment/AssetID follow domain.RiskPatch's tri-state rule:
// absent (nil) leaves the field unchanged; present as "" clears it; present
// as a non-empty string sets it (Treatment validated, AssetID re-verified
// against the caller's tenant first).
type patchRiskRequest struct {
	RowVersion  int64   `json:"row_version"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Category    *string `json:"category"`
	Likelihood  *string `json:"likelihood"`
	Impact      *string `json:"impact"`
	Treatment   *string `json:"treatment"`
	AssetID     *string `json:"asset_id"`
	Status      *string `json:"status"`
}

// nonStatusPatchFieldsSet reports whether req carries any field other than
// status — used to refuse a request that tries to combine a lifecycle
// transition with an ordinary field change, mirroring
// patchAssetRequest.nonStatusPatchFieldsSet exactly.
func (req patchRiskRequest) nonStatusPatchFieldsSet() bool {
	return req.Title != nil || req.Description != nil || req.Category != nil ||
		req.Likelihood != nil || req.Impact != nil || req.Treatment != nil || req.AssetID != nil
}

// listRisks returns a page of the caller's risks, optionally filtered by
// status/category.
func (s *Server) listRisks(w http.ResponseWriter, r *http.Request) {
	if s.risks == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "risk register administration is not configured")
		return
	}
	q := r.URL.Query()
	limit := 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeProblem(w, r, http.StatusUnprocessableEntity, "invalid limit", "limit must be an integer")
			return
		}
		limit = n
	}
	items, err := s.risks.List(r.Context(), limit, q.Get("after"), domain.RiskStatus(q.Get("status")), q.Get("category"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]riskDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toRiskDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createRisk registers a new risk-register entry, always open.
func (s *Server) createRisk(w http.ResponseWriter, r *http.Request) {
	if s.risks == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "risk register administration is not configured")
		return
	}
	var req createRiskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	var treatment *domain.RiskTreatment
	if req.Treatment != nil {
		t := domain.RiskTreatment(strings.TrimSpace(*req.Treatment))
		treatment = &t
	}

	risk, err := domain.NewRisk(tenant.TenantID, req.Title, req.Description, req.Category,
		domain.RiskLikelihood(req.Likelihood), domain.RiskImpact(req.Impact), treatment, req.AssetID)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	created, err := s.risks.Create(r.Context(), risk)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "asset_id does not name an asset visible to this tenant")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toRiskDTO(created))
}

// getRisk returns one risk by identifier.
func (s *Server) getRisk(w http.ResponseWriter, r *http.Request) {
	if s.risks == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "risk register administration is not configured")
		return
	}
	risk, err := s.risks.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such risk")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRiskDTO(risk))
}

// patchRisk changes title/description/category/likelihood/impact/treatment/
// asset_id, OR performs a lifecycle transition — never both (see
// patchRiskRequest).
func (s *Server) patchRisk(w http.ResponseWriter, r *http.Request) {
	if s.risks == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "risk register administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchRiskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version", "must be the row_version last read for this risk"))
		return
	}
	switch {
	case req.Status == nil && !req.nonStatusPatchFieldsSet():
		s.mapError(w, r, domain.NewValidationError("body",
			"supply at least one of title, description, category, likelihood, impact, treatment, asset_id or status"))
		return
	case req.Status != nil && req.nonStatusPatchFieldsSet():
		s.mapError(w, r, domain.NewValidationError("body",
			"a status transition must be supplied on its own; changing both would consume "+
				"two row versions and leave the outcome of one of them unreported"))
		return
	}

	var (
		updated *domain.Risk
		err     error
	)
	if req.Status != nil {
		status := domain.RiskStatus(strings.TrimSpace(*req.Status))
		if !status.Valid() {
			s.mapError(w, r, domain.NewValidationError("status", "must be one of: open, mitigating, accepted, closed"))
			return
		}
		updated, err = s.risks.SetStatus(r.Context(), id, req.RowVersion, status)
	} else {
		patch := domain.RiskPatch{
			Title: req.Title, Description: req.Description, Category: req.Category,
			Treatment: req.Treatment, AssetID: req.AssetID,
		}
		if req.Likelihood != nil {
			l := domain.RiskLikelihood(strings.TrimSpace(*req.Likelihood))
			patch.Likelihood = &l
		}
		if req.Impact != nil {
			imp := domain.RiskImpact(strings.TrimSpace(*req.Impact))
			patch.Impact = &imp
		}
		updated, err = s.risks.Update(r.Context(), id, req.RowVersion, patch)
	}

	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found",
			"no such risk, or asset_id does not name an asset visible to this tenant")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "risk was modified concurrently; re-read and retry")
		return
	case errors.Is(err, domain.ErrInvalidTransition):
		// The target state is well-formed; it is the move that is refused —
		// a conflict with the record's current state, mirrors
		// patchVulnFinding's identical ErrInvalidTransition branch.
		writeProblem(w, r, http.StatusConflict, "conflict", err.Error())
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toRiskDTO(updated))
}

// riskRegisterEntryDTO is one row of GET /v1/admin/risks/register (E8.4a):
// the risk, plus the computed score/band — see domain.RiskRegisterEntry's
// own doc comment. Nothing here is stored.
type riskRegisterEntryDTO struct {
	Risk  riskDTO `json:"risk"`
	Score int     `json:"score"`
	Band  string  `json:"band"`
}

func toRiskRegisterEntryDTO(e *domain.RiskRegisterEntry) riskRegisterEntryDTO {
	return riskRegisterEntryDTO{
		Risk: toRiskDTO(&e.Risk), Score: e.Score, Band: string(e.Band),
	}
}

type riskRegisterResponse struct {
	Items []riskRegisterEntryDTO `json:"items"`
}

// getRiskRegister serves GET /v1/admin/risks/register (E8.4a, ADR-SOC-008):
// a bounded, read-only projection of the caller's OPEN (non-closed) risks
// ranked by likelihood x impact score — domain.RiskRepository.Register's own
// doc comment states the exact ranking. Row-level security on risk is the
// ONLY isolation mechanism — there is no explicit tenant predicate,
// mirroring listPrioritizedVulnFindings exactly.
func (s *Server) getRiskRegister(w http.ResponseWriter, r *http.Request) {
	if s.risks == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "risk register administration is not configured")
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
	items, err := s.risks.Register(r.Context(), limit)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]riskRegisterEntryDTO, 0, len(items))
	for i := range items {
		out = append(out, toRiskRegisterEntryDTO(&items[i]))
	}
	writeJSON(w, http.StatusOK, riskRegisterResponse{Items: out})
}
