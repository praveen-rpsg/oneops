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

// SetInvitations wires the invitation registry. Until it is called the
// endpoints report 501 rather than 404, matching every other admin registry
// in this package.
func (s *Server) SetInvitations(repo domain.InvitationRepository) { s.invitations = repo }

// invitationTTL is the server-side lifetime granted to every invitation minted
// through this endpoint. domain.NewInvitation requires the caller to decide a
// ttl rather than defaulting one internally — see its own doc comment — and
// this is that decision, made once. 7 days balances an admin's typical
// turnaround for inviting someone against the risk window an unredeemed
// bearer token sitting in an email inbox represents.
const invitationTTL = 7 * 24 * time.Hour

// invitationDTO deliberately has no field for the token or its hash — see
// domain.Invitation's own doc comment on why the struct it is built from
// carries neither. tenant_id is likewise omitted, for the same reason
// membershipDTO omits it: the caller is already inside exactly one tenant, so
// echoing the boundary back names an identifier the isolation model does not
// want travelling.
type invitationDTO struct {
	InvitationID string     `json:"invitation_id"`
	OrgID        string     `json:"org_id"`
	Email        string     `json:"email"`
	Status       string     `json:"status"`
	ExpiresAt    time.Time  `json:"expires_at"`
	CreatedAt    time.Time  `json:"created_at"`
	RedeemedAt   *time.Time `json:"redeemed_at,omitempty"`
}

func toInvitationDTO(i *domain.Invitation) invitationDTO {
	return invitationDTO{
		InvitationID: i.InvitationID, OrgID: i.OrgID, Email: i.Email,
		Status: string(i.Status), ExpiresAt: i.ExpiresAt, CreatedAt: i.CreatedAt,
		RedeemedAt: i.RedeemedAt,
	}
}

// createInvitationRequest carries only the address to invite. There is
// deliberately no org_id or tenant_id field: the target organization is
// resolved from the caller's authenticated context, never the request body —
// a caller-supplied org_id here is exactly the cross-tenant key-confusion
// class the Trust Register records as entry 1, applied to an endpoint that
// mints a standing bearer credential rather than a row.
type createInvitationRequest struct {
	Email string `json:"email"`
}

// createInvitationResponse is the ONLY response in this package that carries
// the plaintext token. domain.NewInvitation returns it exactly once and never
// stores it — see its own doc comment — so this is the one and only place a
// caller can obtain it; a client that discards this response has to request a
// new invitation, not recover the old one.
type createInvitationResponse struct {
	invitationDTO
	Token string `json:"token"`
}

// createInvitation invites an email address into the CALLER'S OWN
// organization (E-ID.4a).
//
// The org and tenant are resolved from domain.TenantFrom, exactly as
// getTenantOrganization resolves them, and never from the request: the DTO
// above has no field that could name a different one.
func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request) {
	if s.invitations == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "invitation administration is not configured")
		return
	}
	if s.organizations == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "organization registry is not configured")
		return
	}

	var req createInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	// Length is checked here because domain.Invitation.Validate deliberately
	// does not: it enforces the shape a stored token hash and status must
	// have, and the email pattern, but not this bound. citext has no length
	// limit of its own, so without this check an oversized-but-well-shaped
	// address would be accepted and stored rather than refused with a 422.
	email := strings.TrimSpace(req.Email)
	if email == "" {
		s.mapError(w, r, domain.NewValidationError("email", "must not be empty"))
		return
	}
	if len(email) > domain.MaxEmailLength {
		s.mapError(w, r, domain.NewValidationError("email", "must be at most 254 characters"))
		return
	}

	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}
	org, err := s.organizations.GetByTenant(r.Context(), tenant.TenantID)
	if errors.Is(err, domain.ErrNotFound) {
		// The 1:1 (uq_org_tenant) makes this unreachable in a consistent
		// database; reported as a 404 rather than a panic in case it ever
		// isn't, the same convention getTenantOrganization uses.
		writeProblem(w, r, http.StatusNotFound, "not found", "no organization realises this tenant")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
		return
	}

	inv, token, err := domain.NewInvitation(org.OrgID, tenant.TenantID, email, invitationTTL, time.Now())
	if err != nil {
		s.mapError(w, r, err)
		return
	}

	created, err := s.invitations.Create(r.Context(), inv)
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, createInvitationResponse{
		invitationDTO: toInvitationDTO(created),
		Token:         token,
	})
}

// listInvitations returns a page of the CALLER'S OWN organization's
// invitations. The org is resolved from context exactly as createInvitation
// resolves it — never a query parameter — so there is no org_id a caller
// could substitute to read another tenant's pending invitations.
func (s *Server) listInvitations(w http.ResponseWriter, r *http.Request) {
	if s.invitations == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "invitation administration is not configured")
		return
	}
	if s.organizations == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "organization registry is not configured")
		return
	}

	tenant, ok := domain.TenantFrom(r.Context())
	if !ok {
		writeProblem(w, r, http.StatusForbidden, "forbidden", "no tenant is bound to this request")
		return
	}
	org, err := s.organizations.GetByTenant(r.Context(), tenant.TenantID)
	if errors.Is(err, domain.ErrNotFound) {
		writeProblem(w, r, http.StatusNotFound, "not found", "no organization realises this tenant")
		return
	}
	if err != nil {
		s.mapError(w, r, err)
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

	items, err := s.invitations.ListByOrg(r.Context(), org.OrgID, limit, strings.TrimSpace(r.URL.Query().Get("after")))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	out := make([]invitationDTO, 0, len(items))
	for _, i := range items {
		out = append(out, toInvitationDTO(i))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// revokeInvitation withdraws a pending invitation.
//
// Isolation is the tenant-scoped pool's row-level security, exactly as
// InvitationStore documents on itself: Revoke runs on the connection bound to
// the caller's tenant, so an invitation_id naming another tenant's row matches
// nothing there and comes back domain.ErrNotFound — a 404, never a 403 that
// would confirm the invitation exists somewhere else. This is the same
// reliance revokeMembership places on the identical mechanism for the
// identical reason (membership and invitation are both tenant-owned,
// row-level-secured tables).
func (s *Server) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	if s.invitations == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "invitation administration is not configured")
		return
	}
	revoked, err := s.invitations.Revoke(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.mapError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toInvitationDTO(revoked))
}
