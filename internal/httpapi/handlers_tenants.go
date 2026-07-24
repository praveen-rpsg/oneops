package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/domain"
)

// SetTenants wires the tenant registry. Until it is called the platform
// resolves every request to the system tenant.
func (s *Server) SetTenants(repo domain.TenantRepository) { s.tenants = repo }

type tenantDTO struct {
	TenantID   string    `json:"tenant_id"`
	Slug       string    `json:"slug"`
	Name       string    `json:"name"`
	ExternalID string    `json:"external_id,omitempty"`
	Status     string    `json:"status"`
	RowVersion int64     `json:"row_version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func toTenantDTO(t *domain.Tenant) tenantDTO {
	return tenantDTO{
		TenantID: t.TenantID, Slug: t.Slug, Name: t.Name,
		ExternalID: t.ExternalID, Status: string(t.Status),
		RowVersion: t.RowVersion, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

type createTenantRequest struct {
	Slug       string `json:"slug"`
	Name       string `json:"name"`
	ExternalID string `json:"external_id"`
}

type patchTenantRequest struct {
	Status string `json:"status"`
}

// listTenants returns every registered tenant.
func (s *Server) listTenants(w http.ResponseWriter, r *http.Request) {
	if s.tenants == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "tenant registry is not configured")
		return
	}
	list, err := s.tenants.List(r.Context())
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]tenantDTO, 0, len(list))
	for _, t := range list {
		out = append(out, toTenantDTO(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createTenant registers a new isolation boundary.
func (s *Server) createTenant(w http.ResponseWriter, r *http.Request) {
	if s.tenants == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "tenant registry is not configured")
		return
	}
	var req createTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}

	t := &domain.Tenant{
		// Identifiers are minted here rather than accepted from the caller, so a
		// client cannot choose an id that collides with the system tenant.
		TenantID:   ulid.Make().String(),
		Slug:       strings.ToLower(strings.TrimSpace(req.Slug)),
		Name:       strings.TrimSpace(req.Name),
		ExternalID: strings.TrimSpace(req.ExternalID),
		Status:     domain.TenantActive,
	}
	if err := t.Validate(); err != nil {
		s.mapError(w, r, err)
		return
	}

	created, err := s.tenants.Create(r.Context(), t)
	if errors.Is(err, domain.ErrConflict) {
		writeProblem(w, r, http.StatusConflict, "conflict", "slug or external_id already registered")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTenantDTO(created))
}

// patchTenant suspends or reactivates a tenant.
//
// Suspension is the only mutation the registry exposes. A tenant is never
// deleted: its audit history is append-only by constitutional guarantee, so
// removing the tenant would orphan records that cannot themselves be removed.
func (s *Server) patchTenant(w http.ResponseWriter, r *http.Request) {
	if s.tenants == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "tenant registry is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	status := domain.TenantStatus(strings.TrimSpace(req.Status))
	if !status.Valid() {
		s.mapError(w, r, domain.NewValidationError("status", "must be one of: active, suspended"))
		return
	}

	current, err := s.tenants.Get(r.Context(), id)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such tenant")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	// The system tenant owns every pre-tenancy row and is the fallback for
	// tokens asserting no tenant. Suspending it would lock the platform out of
	// its own data with no way back in through the API.
	if current.TenantID == domain.SystemTenantID && status == domain.TenantSuspended {
		writeProblem(w, r, http.StatusConflict, "conflict", "the system tenant cannot be suspended")
		return
	}

	updated, err := s.tenants.SetStatus(r.Context(), id, current.RowVersion, status)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such tenant")
		return
	}
	if errors.Is(err, domain.ErrVersionMismatch) {
		writeProblem(w, r, http.StatusConflict, "conflict", "tenant was modified concurrently")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTenantDTO(updated))
}
