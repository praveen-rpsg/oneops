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

// SetComplianceControls wires the compliance-control repository (E8.4b).
// Until it is called the endpoints report 501 rather than 404, the same
// convention SetRisks/SetIncidents establish.
func (s *Server) SetComplianceControls(repo domain.ComplianceControlRepository) {
	s.complianceControls = repo
}

type complianceControlDTO struct {
	ControlID   string    `json:"control_id"`
	Framework   string    `json:"framework"`
	ControlRef  string    `json:"control_ref"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	RowVersion  int64     `json:"row_version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// toComplianceControlDTO deliberately omits tenant_id, the same choice
// toRiskDTO/toIncidentDTO make: the caller is already inside exactly one
// tenant.
func toComplianceControlDTO(c *domain.ComplianceControl) complianceControlDTO {
	return complianceControlDTO{
		ControlID: c.ControlID, Framework: c.Framework, ControlRef: c.ControlRef,
		Title: c.Title, Description: c.Description, Status: string(c.Status),
		RowVersion: c.RowVersion, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt,
	}
}

type controlEvidenceDTO struct {
	EvidenceID string    `json:"evidence_id"`
	Kind       string    `json:"kind"`
	Value      string    `json:"value"`
	RecordedBy string    `json:"recorded_by"`
	RecordedAt time.Time `json:"recorded_at"`
}

func toControlEvidenceDTO(e *domain.ControlEvidence) controlEvidenceDTO {
	return controlEvidenceDTO{
		EvidenceID: e.EvidenceID, Kind: string(e.Kind), Value: e.Value,
		RecordedBy: e.RecordedBy, RecordedAt: e.RecordedAt,
	}
}

// complianceControlWithEvidenceDTO is GET /v1/admin/compliance-controls/{id}'s
// response shape: the control plus its append-only evidence trail, bounded
// (ComplianceControlRepository.Evidence's own doc comment) — evidence has no
// separate paginated endpoint of its own.
type complianceControlWithEvidenceDTO struct {
	complianceControlDTO
	Evidence []controlEvidenceDTO `json:"evidence"`
}

// createComplianceControlRequest carries no control_id: the identifier is
// minted server-side, the same rule every other create route in this schema
// follows.
type createComplianceControlRequest struct {
	Framework   string `json:"framework"`
	ControlRef  string `json:"control_ref"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// patchComplianceControlRequest changes title/description in a single call,
// OR performs exactly one lifecycle transition — never both in the same
// request (E8.4b), mirroring patchRiskRequest's identical split. framework/
// control_ref are absent: they are immutable after Create (see
// domain.ComplianceControl's doc comment).
type patchComplianceControlRequest struct {
	RowVersion  int64   `json:"row_version"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Status      *string `json:"status"`
}

func (req patchComplianceControlRequest) nonStatusPatchFieldsSet() bool {
	return req.Title != nil || req.Description != nil
}

// listComplianceControls returns a page of the caller's controls, optionally
// filtered by framework/status.
func (s *Server) listComplianceControls(w http.ResponseWriter, r *http.Request) {
	if s.complianceControls == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "compliance control administration is not configured")
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
	items, err := s.complianceControls.List(r.Context(), limit, q.Get("after"), q.Get("framework"), domain.ComplianceControlStatus(q.Get("status")))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]complianceControlDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toComplianceControlDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createComplianceControl registers a new compliance control, always
// not_implemented.
func (s *Server) createComplianceControl(w http.ResponseWriter, r *http.Request) {
	if s.complianceControls == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "compliance control administration is not configured")
		return
	}
	var req createComplianceControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	control, err := domain.NewComplianceControl(tenant.TenantID, req.Framework, req.ControlRef, req.Title, req.Description)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	created, err := s.complianceControls.Create(r.Context(), control)
	if errors.Is(err, domain.ErrConflict) {
		writeProblem(w, r, http.StatusConflict, "conflict",
			"this tenant already tracks a control with the same framework and control_ref")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toComplianceControlDTO(created))
}

// getComplianceControl returns one control by identifier, together with its
// append-only evidence trail — see complianceControlWithEvidenceDTO's own
// doc comment for why evidence is embedded rather than a separate endpoint.
func (s *Server) getComplianceControl(w http.ResponseWriter, r *http.Request) {
	if s.complianceControls == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "compliance control administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")
	control, err := s.complianceControls.Get(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such compliance control")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	evidence, err := s.complianceControls.Evidence(r.Context(), id, 0)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]controlEvidenceDTO, 0, len(evidence))
	for _, e := range evidence {
		out = append(out, toControlEvidenceDTO(e))
	}
	writeJSON(w, http.StatusOK, complianceControlWithEvidenceDTO{
		complianceControlDTO: toComplianceControlDTO(control),
		Evidence:             out,
	})
}

// patchComplianceControl changes title/description, OR performs a lifecycle
// transition — never both (see patchComplianceControlRequest).
func (s *Server) patchComplianceControl(w http.ResponseWriter, r *http.Request) {
	if s.complianceControls == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "compliance control administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchComplianceControlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version", "must be the row_version last read for this compliance control"))
		return
	}
	switch {
	case req.Status == nil && !req.nonStatusPatchFieldsSet():
		s.mapError(w, r, domain.NewValidationError("body", "supply at least one of title, description or status"))
		return
	case req.Status != nil && req.nonStatusPatchFieldsSet():
		s.mapError(w, r, domain.NewValidationError("body",
			"a status transition must be supplied on its own; changing both would consume "+
				"two row versions and leave the outcome of one of them unreported"))
		return
	}

	var (
		updated *domain.ComplianceControl
		err     error
	)
	if req.Status != nil {
		status := domain.ComplianceControlStatus(strings.TrimSpace(*req.Status))
		if !status.Valid() {
			s.mapError(w, r, domain.NewValidationError("status",
				"must be one of: not_implemented, in_progress, implemented, not_applicable"))
			return
		}
		updated, err = s.complianceControls.SetStatus(r.Context(), id, req.RowVersion, status)
	} else {
		updated, err = s.complianceControls.Update(r.Context(), id, req.RowVersion, domain.ComplianceControlPatch{
			Title: req.Title, Description: req.Description,
		})
	}

	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such compliance control")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "compliance control was modified concurrently; re-read and retry")
		return
	case errors.Is(err, domain.ErrInvalidTransition):
		// The target state is well-formed; it is the move that is refused —
		// a conflict with the record's current state, mirrors
		// patchRisk's identical ErrInvalidTransition branch.
		writeProblem(w, r, http.StatusConflict, "conflict", err.Error())
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toComplianceControlDTO(updated))
}

// addControlEvidenceRequest carries no evidence_id/recorded_by: the
// identifier is minted server-side and the actor is sourced from the
// authenticated request subject, never from request data (see
// domain.ControlEvidence's doc comment).
type addControlEvidenceRequest struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// addControlEvidence appends one evidence record to a control's trail
// (E8.4b). The control is re-verified against the caller's own tenant
// before the write — ComplianceControlRepository.AddEvidence's own doc
// comment.
func (s *Server) addControlEvidence(w http.ResponseWriter, r *http.Request) {
	if s.complianceControls == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "compliance control administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req addControlEvidenceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}

	kind := domain.ControlEvidenceKind(strings.TrimSpace(req.Kind))
	if !kind.Valid() {
		s.mapError(w, r, domain.NewValidationError("kind", "must be one of: url, note, attestation"))
		return
	}

	created, err := s.complianceControls.AddEvidence(r.Context(), id, kind, req.Value)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such compliance control")
		return
	case errors.Is(err, domain.ErrNoActor):
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no authenticated subject is bound to this request")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toControlEvidenceDTO(created))
}
