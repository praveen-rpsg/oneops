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

// SetUsers wires the user registry. Until it is called the endpoints report 501
// rather than 404, so a deployment that has not enabled identity says so
// instead of looking like a routing mistake.
func (s *Server) SetUsers(repo domain.UserRepository) { s.users = repo }

type userDTO struct {
	UserID      string    `json:"user_id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name"`
	Status      string    `json:"status"`
	RowVersion  int64     `json:"row_version"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func toUserDTO(u *domain.User) userDTO {
	return userDTO{
		UserID: u.UserID, Email: u.Email, DisplayName: u.DisplayName,
		Status: string(u.Status), RowVersion: u.RowVersion,
		CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt,
	}
}

// createUserRequest carries no user_id: the identifier is minted server-side so
// a client cannot choose one, which is the cross-tenant key-confusion defect the
// Trust Register records as entry 1.
type createUserRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
}

// patchUserRequest changes exactly one thing per call.
//
// The fields are pointers so an absent field is distinguishable from an empty
// one — clearing a display name and not touching it are different requests, and
// a non-pointer string cannot express the difference.
type patchUserRequest struct {
	RowVersion  int64   `json:"row_version"`
	DisplayName *string `json:"display_name"`
	Status      *string `json:"status"`
}

// listUsers returns a page of users, or resolves one by address when `email` is
// supplied.
//
// The lookup is a filter on the collection rather than its own route: an
// address contains characters that need escaping in a path segment, and a
// second route would be a second place that decides what "the user with this
// address" means.
func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	if s.users == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "user registry is not configured")
		return
	}

	if email := strings.TrimSpace(r.URL.Query().Get("email")); email != "" {
		u, err := s.users.GetByEmail(r.Context(), email)
		if errors.Is(err, domain.ErrNotFound) {
			// An empty page, not a 404. The collection exists; nothing in it
			// matched, and a caller filtering a list expects a list back.
			writeJSON(w, http.StatusOK, map[string]any{"items": []userDTO{}})
			return
		}
		if err != nil {
			s.mapError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": []userDTO{toUserDTO(u)}})
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

	list, err := s.users.List(r.Context(), limit, strings.TrimSpace(r.URL.Query().Get("after")))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]userDTO, 0, len(list))
	for _, u := range list {
		out = append(out, toUserDTO(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// createUser registers a person. The user starts invited and holds no access
// until a membership is granted (ADR-IDENTITY-001 §8.3).
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	if s.users == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "user registry is not configured")
		return
	}

	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}

	// Construction, normalisation and validation all live in the aggregate.
	// Repeating any of it here would be a second answer to what a valid user is.
	u, err := domain.NewUser(req.Email, req.DisplayName)
	if err != nil {
		s.mapError(w, r, err)
		return
	}

	created, err := s.users.Create(r.Context(), u)
	if errors.Is(err, domain.ErrConflict) {
		writeProblem(w, r, http.StatusConflict, "conflict", "email address already registered")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toUserDTO(created))
}

// getUser returns one user by platform identifier.
func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	if s.users == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "user registry is not configured")
		return
	}

	u, err := s.users.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no such user")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(u))
}

// patchUser changes a display name or performs a lifecycle transition — one or
// the other, never both.
//
// The two go through different repository methods on purpose: SetStatus is the
// single place a transition is checked, and a call that did both would consume
// two row versions and leave the caller unable to say which half failed.
//
// row_version is required rather than re-read here. A console where two admins
// hold the same user open is exactly where a lost update happens, and the
// repository already takes the version as a parameter for this reason.
func (s *Server) patchUser(w http.ResponseWriter, r *http.Request) {
	if s.users == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "user registry is not configured")
		return
	}
	id := chi.URLParam(r, "id")

	var req patchUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request", "malformed JSON body")
		return
	}

	if req.RowVersion < 1 {
		s.mapError(w, r, domain.NewValidationError("row_version",
			"must be the row_version last read for this user"))
		return
	}
	switch {
	case req.DisplayName == nil && req.Status == nil:
		s.mapError(w, r, domain.NewValidationError("body",
			"supply exactly one of display_name or status"))
		return
	case req.DisplayName != nil && req.Status != nil:
		s.mapError(w, r, domain.NewValidationError("body",
			"supply exactly one of display_name or status; changing both would consume "+
				"two row versions and leave the outcome of one of them unreported"))
		return
	}

	var (
		updated *domain.User
		err     error
	)
	if req.DisplayName != nil {
		updated, err = s.users.UpdateProfile(r.Context(), id, req.RowVersion, strings.TrimSpace(*req.DisplayName))
	} else {
		// An undefined status is a malformed request, not a refused move, and is
		// rejected here rather than after a round trip — the same order
		// patchTenant uses. The repository checks it too: this is the transport
		// declining to ask a question whose answer is already known.
		status := domain.UserStatus(strings.TrimSpace(*req.Status))
		if !status.Valid() {
			s.mapError(w, r, domain.NewValidationError("status",
				"must be one of: invited, active, suspended, deactivated"))
			return
		}
		updated, err = s.users.SetStatus(r.Context(), id, req.RowVersion, status)
	}

	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", "no such user")
		return
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusConflict, "conflict", "user was modified concurrently; re-read and retry")
		return
	case err != nil:
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toUserDTO(updated))
}
