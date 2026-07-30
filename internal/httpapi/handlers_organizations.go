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

// SetOrganizations wires the organisation registry. Until it is called the
// endpoints report 501 rather than 404, so a deployment that has not enabled
// identity says so instead of looking like a routing mistake.
func (s *Server) SetOrganizations(repo domain.OrganizationRepository) { s.organizations = repo }

type organizationDTO struct {
	OrgID string `json:"org_id"`
	// TenantID is exposed because an operator administering an organisation
	// needs the boundary it is realised as in order to read anything inside it.
	// It is never accepted on the way in — see createOrganizationRequest.
	TenantID   string    `json:"tenant_id"`
	Slug       string    `json:"slug"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	RowVersion int64     `json:"row_version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toOrganizationDTO(o *domain.Organization) organizationDTO {
	return organizationDTO{
		OrgID: o.OrgID, TenantID: o.TenantID, Slug: o.Slug, Name: o.Name,
		Status: string(o.Status), RowVersion: o.RowVersion,
		CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
}

// createOrganizationRequest carries neither identifier. Both are minted
// server-side by domain.NewOrganization: a caller-chosen tenant_id could name
// the system tenant and a caller-chosen org_id is the global key space the
// Trust Register records as entry 1.
type createOrganizationRequest struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// patchOrganizationRequest performs a lifecycle transition.
//
// Status is the only mutation the registry exposes, because SetStatus is the
// only mutation OrganizationRepository has: renaming is not a modelled
// operation, and inventing one here would put a second answer to what an
// organisation is in the transport layer.
type patchOrganizationRequest struct {
	RowVersion int64  `json:"row_version"`
	Status     string `json:"status"`
}

// listOrganizations returns a page, or resolves one by slug when `slug` is
// supplied.
//
// The lookup is a filter on the collection rather than its own route, for the
// same reason the user registry resolves an address that way: a second route
// would be a second place deciding what "the organisation with this slug"
// means.
func (s *Server) listOrganizations(w http.ResponseWriter, r *http.Request) {
	if s.organizations == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "organization registry is not configured")
		return
	}

	if slug := strings.TrimSpace(r.URL.Query().Get("slug")); slug != "" {
		o, err := s.organizations.GetBySlug(r.Context(), slug)
		if errors.Is(err, domain.ErrNotFound) {
			// An empty page, not a 404. The collection exists; nothing in it
			// matched, and a caller filtering a list expects a list back.
			writeJSON(w, http.StatusOK, map[string]any{"items": []organizationDTO{}})
			return
		}
		if err != nil {
			s.mapError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": []organizationDTO{toOrganizationDTO(o)}})
		return
	}

	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			s.mapError(w, r, domain.NewValidationError("limit", "must be a positive integer"))
			return
		}
		limit = n
	}

	list, err := s.organizations.List(r.Context(), limit, strings.TrimSpace(r.URL.Query().Get("after")))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]organizationDTO, 0, len(list))
	for _, o := range list {
		out = append(out, toOrganizationDTO(o))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createOrganization registers an Identity scope and the Tenant realising it.
//
// The two rows are written in one transaction by the repository
// (ADR-IDENTITY-001 §7.1); this handler does not create the tenant separately,
// because a caller that could observe the gap could address an organisation
// with no isolation boundary.
func (s *Server) createOrganization(w http.ResponseWriter, r *http.Request) {
	if s.organizations == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "organization registry is not configured")
		return
	}

	var req createOrganizationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}

	// Construction, normalisation and validation all live in the aggregate.
	// Repeating any of it here would be a second answer to what a valid
	// organisation is.
	o, err := domain.NewOrganization(req.Name, req.Slug)
	if err != nil {
		s.mapError(w, r, err)
		return
	}

	created, err := s.organizations.Create(r.Context(), o)
	if errors.Is(err, domain.ErrConflict) {
		// The slug is unique on organization AND on tenant, and the tenant row
		// is written first, so a slug already held by a tenant conflicts here
		// too. Saying so is more useful than "organization already exists".
		writeProblem(w, r, http.StatusConflict, "conflict", "slug already in use by an organization or tenant")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toOrganizationDTO(created))
}

// getOrganization returns one organisation by platform identifier.
func (s *Server) getOrganization(w http.ResponseWriter, r *http.Request) {
	if s.organizations == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "organization registry is not configured")
		return
	}

	o, err := s.organizations.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such organization")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrganizationDTO(o))
}

// patchOrganization suspends or reactivates an organisation, cascading to the
// realising tenant.
//
// row_version is required rather than re-read here: a console where two
// operators hold the same organisation open is exactly where a lost update
// happens, and suspension is not an update anyone should apply to a record they
// have not seen.
func (s *Server) patchOrganization(w http.ResponseWriter, r *http.Request) {
	if s.organizations == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "organization registry is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchOrganizationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}

	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version",
			"must be the row_version last read for this organization"))
		return
	}

	// An undefined status is a malformed request and is rejected before the
	// round trip, the same order patchUser and patchTenant use. The repository
	// checks it too: this is the transport declining to ask a question whose
	// answer is already known.
	status := domain.OrganizationStatus(strings.TrimSpace(req.Status))
	if !status.Valid() {
		s.mapError(w, r, domain.NewValidationError("status", "must be one of: active, suspended"))
		return
	}

	updated, err := s.organizations.SetStatus(r.Context(), id, req.RowVersion, status)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such organization")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict",
			"organization was modified concurrently; re-read and retry")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toOrganizationDTO(updated))
}
