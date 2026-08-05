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

// SetMaintenanceWindows wires the maintenance window repository (E3.3a).
// Until it is called the endpoints report 501 rather than 404, the same
// convention SetAlertRules/SetCollectorChecks establish.
func (s *Server) SetMaintenanceWindows(repo domain.MaintenanceWindowRepository) {
	s.maintenanceWindows = repo
}

// maintenanceWindowDTO deliberately omits tenant_id, the same choice
// toAlertRuleDTO makes: the caller is already inside exactly one tenant.
type maintenanceWindowDTO struct {
	WindowID         string     `json:"window_id"`
	AssetID          string     `json:"asset_id"`
	StartsAt         time.Time  `json:"starts_at"`
	EndsAt           time.Time  `json:"ends_at"`
	Reason           string     `json:"reason"`
	CreatedBy        string     `json:"created_by"`
	SuppressedCount  int64      `json:"suppressed_count"`
	LastSuppressedAt *time.Time `json:"last_suppressed_at,omitempty"`
	RowVersion       int64      `json:"row_version"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

func toMaintenanceWindowDTO(w *domain.MaintenanceWindow) maintenanceWindowDTO {
	dto := maintenanceWindowDTO{
		WindowID: w.WindowID, AssetID: w.AssetID, StartsAt: w.StartsAt, EndsAt: w.EndsAt,
		Reason: w.Reason, CreatedBy: w.CreatedBy, SuppressedCount: w.SuppressedCount,
		RowVersion: w.RowVersion, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt,
	}
	if !w.LastSuppressedAt.IsZero() {
		t := w.LastSuppressedAt
		dto.LastSuppressedAt = &t
	}
	return dto
}

// createMaintenanceWindowRequest carries no window_id (minted server-side,
// the same rule createAlertRuleRequest follows) and no created_by/
// suppressed_count/last_suppressed_at (repository/evaluator-owned — see
// domain.MaintenanceWindow's doc comment).
type createMaintenanceWindowRequest struct {
	AssetID  string    `json:"asset_id"`
	StartsAt time.Time `json:"starts_at"`
	EndsAt   time.Time `json:"ends_at"`
	Reason   string    `json:"reason"`
}

// listMaintenanceWindows returns a page of the caller's maintenance windows.
func (s *Server) listMaintenanceWindows(w http.ResponseWriter, r *http.Request) {
	if s.maintenanceWindows == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "maintenance window administration is not configured")
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
	items, err := s.maintenanceWindows.List(r.Context(), limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]maintenanceWindowDTO, 0, len(items))
	for _, item := range items {
		out = append(out, toMaintenanceWindowDTO(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createMaintenanceWindow declares a one-shot [starts_at, ends_at)
// suppression window over one asset (E3.3a).
func (s *Server) createMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	if s.maintenanceWindows == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "maintenance window administration is not configured")
		return
	}
	var req createMaintenanceWindowRequest
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

	win, err := domain.NewMaintenanceWindow(tenant.TenantID, req.AssetID, req.Reason, req.StartsAt, req.EndsAt)
	if err != nil {
		s.mapError(w, r, err)
		return
	}

	created, err := s.maintenanceWindows.Create(r.Context(), win)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "asset_id does not name an asset visible to this tenant")
		return
	}
	if errors.Is(err, domain.ErrNoActor) {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no authenticated actor is bound to this request")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toMaintenanceWindowDTO(created))
}

// getMaintenanceWindow returns one window by identifier.
func (s *Server) getMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	if s.maintenanceWindows == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "maintenance window administration is not configured")
		return
	}
	win, err := s.maintenanceWindows.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such maintenance window")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toMaintenanceWindowDTO(win))
}

// deleteMaintenanceWindow cancels/withdraws a window.
func (s *Server) deleteMaintenanceWindow(w http.ResponseWriter, r *http.Request) {
	if s.maintenanceWindows == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "maintenance window administration is not configured")
		return
	}
	err := s.maintenanceWindows.Delete(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such maintenance window")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
