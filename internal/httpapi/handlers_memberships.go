package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rpsg/oneops/internal/domain"
)

// SetMemberships wires the membership repository.
func (s *Server) SetMemberships(repo domain.MembershipRepository) { s.memberships = repo }

type membershipDTO struct {
	MembershipID string    `json:"membership_id"`
	OrgID        string    `json:"org_id"`
	UserID       string    `json:"user_id"`
	Status       string    `json:"status"`
	RowVersion   int64     `json:"row_version"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// toMembershipDTO deliberately omits tenant_id. The caller is already inside
// exactly one tenant — the connection serving them is bound to it — so echoing
// the boundary back tells them nothing they can act on and names an identifier
// the isolation model does not want travelling.
func toMembershipDTO(m *domain.Membership) membershipDTO {
	return membershipDTO{
		MembershipID: m.MembershipID, OrgID: m.OrgID, UserID: m.UserID,
		Status: string(m.Status), RowVersion: m.RowVersion,
		CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt,
	}
}

// grantMembershipRequest carries no membership_id: the identifier is minted
// server-side so a caller cannot choose one.
type grantMembershipRequest struct {
	OrgID  string `json:"org_id"`
	UserID string `json:"user_id"`
}

// listMemberships returns a page of an organisation's memberships.
//
// Row-level security confines the result to the caller's tenant, so an org_id
// belonging to someone else returns an empty page rather than a refusal — the
// row is invisible, not forbidden, and the two are deliberately
// indistinguishable to the caller.
func (s *Server) listMemberships(w http.ResponseWriter, r *http.Request) {
	if s.memberships == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented",
			"membership administration is not configured")
		return
	}
	orgID := r.URL.Query().Get("org_id")
	if orgID == "" {
		writeProblem(w, r, http.StatusUnprocessableEntity, "invalid request",
			"org_id is required")
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeProblem(w, r, http.StatusUnprocessableEntity, "invalid limit",
				"limit must be an integer")
			return
		}
		limit = n
	}

	items, err := s.memberships.ListByOrg(r.Context(), orgID, limit, r.URL.Query().Get("after"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]membershipDTO, 0, len(items))
	for _, m := range items {
		out = append(out, toMembershipDTO(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// grantMembership binds a user to an organisation.
func (s *Server) grantMembership(w http.ResponseWriter, r *http.Request) {
	if s.memberships == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented",
			"membership administration is not configured")
		return
	}
	var req grantMembershipRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	// The tenant comes from the bound connection, never from the caller. A
	// request that could name its own tenant_id could plant a membership inside
	// another boundary, which is the key-confusion class the Trust Register
	// records as entry 1.
	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}

	m, err := domain.NewMembership(req.OrgID, tenant.TenantID, req.UserID)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	granted, err := s.memberships.Grant(r.Context(), m)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toMembershipDTO(granted))
}

// revokeMembership withdraws a membership.
//
// The row is not deleted. Revocation is a state change so the administrative
// record of the grant keeps a subject to point at (ADR-IDENTITY-001 §8.3); the
// response returns the revoked membership rather than an empty 204 so the
// caller can see that.
func (s *Server) revokeMembership(w http.ResponseWriter, r *http.Request) {
	if s.memberships == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented",
			"membership administration is not configured")
		return
	}
	revoked, err := s.memberships.Revoke(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toMembershipDTO(revoked))
}
