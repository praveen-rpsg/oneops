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

// SetTeams wires the team repository. Until it is called the endpoints report
// 501 rather than 404, so a deployment that has not enabled identity says so
// instead of looking like a routing mistake.
func (s *Server) SetTeams(repo domain.TeamRepository) { s.teams = repo }

type teamDTO struct {
	TeamID     string    `json:"team_id"`
	OrgID      string    `json:"org_id"`
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	RowVersion int64     `json:"row_version"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// toTeamDTO deliberately omits tenant_id, the same choice toMembershipDTO
// makes: the caller is already inside exactly one tenant, so echoing the
// boundary back tells them nothing they can act on.
func toTeamDTO(t *domain.Team) teamDTO {
	return teamDTO{
		TeamID: t.TeamID, OrgID: t.OrgID, Name: t.Name,
		Status: string(t.Status), RowVersion: t.RowVersion,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

type teamMembershipDTO struct {
	TeamMembershipID string    `json:"team_membership_id"`
	TeamID           string    `json:"team_id"`
	UserID           string    `json:"user_id"`
	RowVersion       int64     `json:"row_version"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

func toTeamMembershipDTO(m *domain.TeamMembership) teamMembershipDTO {
	return teamMembershipDTO{
		TeamMembershipID: m.TeamMembershipID, TeamID: m.TeamID, UserID: m.UserID,
		RowVersion: m.RowVersion, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// createTeamRequest carries no team_id: the identifier is minted server-side
// so a caller cannot choose one.
type createTeamRequest struct {
	OrgID string `json:"org_id"`
	Name  string `json:"name"`
}

// patchTeamRequest changes exactly one thing per call, the same shape
// patchUserRequest uses and for the same reason: the fields are pointers so an
// absent field is distinguishable from an empty one.
type patchTeamRequest struct {
	RowVersion int64   `json:"row_version"`
	Name       *string `json:"name"`
	Status     *string `json:"status"`
}

// addTeamMemberRequest carries no team_membership_id: minted server-side.
type addTeamMemberRequest struct {
	UserID string `json:"user_id"`
}

// listTeams returns a page of an organisation's teams.
//
// Row-level security confines the result to the caller's tenant, so an org_id
// belonging to someone else returns an empty page rather than a refusal — the
// row is invisible, not forbidden (the same behaviour listMemberships has).
func (s *Server) listTeams(w http.ResponseWriter, r *http.Request) {
	if s.teams == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "team administration is not configured")
		return
	}
	orgID := r.URL.Query().Get("org_id")
	if orgID == "" {
		writeProblem(w, r, http.StatusUnprocessableEntity, "invalid request", "org_id is required")
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

	items, err := s.teams.ListByOrg(r.Context(), orgID, limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]teamDTO, 0, len(items))
	for _, t := range items {
		out = append(out, toTeamDTO(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createTeam registers a team within an organisation.
func (s *Server) createTeam(w http.ResponseWriter, r *http.Request) {
	if s.teams == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "team administration is not configured")
		return
	}
	var req createTeamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	// The tenant comes from the bound connection, never from the caller — the
	// same rule grantMembership enforces, for the same reason: a request that
	// could name its own tenant_id could plant a team inside another boundary.
	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	t, err := domain.NewTeam(req.OrgID, tenant.TenantID, req.Name)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	created, err := s.teams.Create(r.Context(), t)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTeamDTO(created))
}

// getTeam returns one team by identifier.
func (s *Server) getTeam(w http.ResponseWriter, r *http.Request) {
	if s.teams == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "team administration is not configured")
		return
	}
	t, err := s.teams.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such team")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTeamDTO(t))
}

// patchTeam renames a team or performs a lifecycle transition — one or the
// other, never both. This mirrors patchUser exactly: the two go through
// different repository methods so a call cannot consume two row versions and
// leave the caller unable to say which half failed.
func (s *Server) patchTeam(w http.ResponseWriter, r *http.Request) {
	if s.teams == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "team administration is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchTeamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}
	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version",
			"must be the row_version last read for this team"))
		return
	}
	switch {
	case req.Name == nil && req.Status == nil:
		s.mapError(w, r, domain.NewValidationError("body", "supply exactly one of name or status"))
		return
	case req.Name != nil && req.Status != nil:
		s.mapError(w, r, domain.NewValidationError("body",
			"supply exactly one of name or status; changing both would consume "+
				"two row versions and leave the outcome of one of them unreported"))
		return
	}

	var (
		updated *domain.Team
		err     error
	)
	if req.Name != nil {
		updated, err = s.teams.UpdateProfile(r.Context(), id, req.RowVersion, strings.TrimSpace(*req.Name))
	} else {
		status := domain.TeamStatus(strings.TrimSpace(*req.Status))
		if !status.Valid() {
			s.mapError(w, r, domain.NewValidationError("status", "must be one of: active, archived"))
			return
		}
		updated, err = s.teams.SetStatus(r.Context(), id, req.RowVersion, status)
	}

	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such team")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "team was modified concurrently; re-read and retry")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toTeamDTO(updated))
}

// listTeamMembers returns a page of a team's members.
func (s *Server) listTeamMembers(w http.ResponseWriter, r *http.Request) {
	if s.teams == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "team administration is not configured")
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

	items, err := s.teams.ListMembers(r.Context(), chi.URLParam(r, "id"), limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]teamMembershipDTO, 0, len(items))
	for _, m := range items {
		out = append(out, toTeamMembershipDTO(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// addTeamMember binds a user to a team.
func (s *Server) addTeamMember(w http.ResponseWriter, r *http.Request) {
	if s.teams == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "team administration is not configured")
		return
	}
	var req addTeamMemberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	m, err := domain.NewTeamMembership(chi.URLParam(r, "id"), tenant.TenantID, req.UserID)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	added, err := s.teams.AddMember(r.Context(), m)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTeamMembershipDTO(added))
}

// removeTeamMember unbinds a user from a team.
func (s *Server) removeTeamMember(w http.ResponseWriter, r *http.Request) {
	if s.teams == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "team administration is not configured")
		return
	}
	err := s.teams.RemoveMember(r.Context(), chi.URLParam(r, "id"), chi.URLParam(r, "userId"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such team member")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
