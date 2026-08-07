package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/rpsg/oneops/internal/domain"
)

// SetRedemptions wires the unauthenticated invitation-redemption path
// (E-ID.4b). Until it is called, POST /auth/invitations/redeem answers 501,
// matching every other registry in this package.
//
// The repository behind this MUST be built from the PRIVILEGED pool — see
// domain.RedemptionRepository's and postgres.RedemptionStore's own doc
// comments for why: the invitee holds no membership yet, so there is no
// tenant to bind before the invitation is found, exactly the ordering
// InvitationRepository.Consume already documents for the admin store's own
// exception (ADR-TENANCY-001 §4).
func (s *Server) SetRedemptions(repo domain.RedemptionRepository) { s.redemptions = repo }

// redeemInvitationRequest carries ONLY the token.
//
// This is a hard security invariant, not a convenience: possession of the
// token is the entire authorization for this endpoint, and every other fact
// the transaction needs — organization, tenant, email — comes from the
// invitation row the token resolves to, never from anything a caller
// supplies (postgres.RedemptionStore.Redeem). There is deliberately no
// display_name field either: this is the single most security-sensitive
// endpoint in the platform, and a display name is not worth widening its
// input surface for. An invitee can set one afterward through their own
// profile; RedemptionRepository.Redeem already ignores any name supplied
// here for an account that already exists.
type redeemInvitationRequest struct {
	Token string `json:"token"`
}

// redeemInvitationResponse is deliberately minimal.
//
// The invitee already knows which link they followed, so no user id,
// membership id or invitation id is returned — each would be one more
// identifier available to whoever holds, or is guessing, a token. The
// organisation's display name is the one fact worth confirming: it says what
// the token was for, scoped to exactly what possession of a valid token
// already proves, and it reveals nothing to a caller who does not hold one.
type redeemInvitationResponse struct {
	Organization string `json:"organization"`
}

// redeemInvitation turns a bearer invitation token into an active account and
// an active membership, atomically (E-ID.4b; ADR pending — follows
// ADR-IAC-003, which reserved this story).
//
// It is the single most security-sensitive endpoint on this service:
// UNAUTHENTICATED by construction — there is no bearer token to present, this
// endpoint is how one becomes able to obtain access at all — so possession of
// the 32 bytes of entropy in the request body IS the authorization. See
// routesFor for why it cannot sit under the authenticated /v1 group and what
// still gates it (invariantGate, rateLimit) in its place.
//
// Every failure — a token that never existed, one already redeemed, one
// revoked, one expired, or one naming a suspended/deactivated account —
// reaches s.mapError as the exact same domain.ErrTokenNotRedeemable and
// therefore produces the exact same response. Distinguishing any of them
// would turn this endpoint into an oracle: for enumerating which tokens are
// real, or for confirming that a given email address already has an account.
// That discipline lives once, in domain.ErrTokenNotRedeemable's own doc
// comment and in RedemptionRepository.Redeem; it is not re-decided here.
func (s *Server) redeemInvitation(w http.ResponseWriter, r *http.Request) {
	if s.redemptions == nil {
		writeProblem(w, r, http.StatusNotImplemented, "not implemented", "invitation redemption is not configured")
		return
	}

	// Cap the request body. This is an UNAUTHENTICATED endpoint and the only
	// field is a ~43-char token, so 4 KiB is generous. Without this an
	// attacker could stream an arbitrarily large body and force pre-provision
	// allocation (hashing the whole thing) on the platform's highest-exposure
	// surface — a DoS/OOM path the per-tenant rate limiter (which bounds count,
	// not size, and shares one bucket for all anonymous callers) does not close
	// (E-ID.4b security review, finding #1).
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)

	var req redeemInvitationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeProblem(w, r, http.StatusRequestEntityTooLarge, "request too large", "body must be JSON")
			return
		}
		writeProblem(w, r, http.StatusBadRequest, "invalid request body", "body must be JSON")
		return
	}

	// displayName is deliberately always empty: this request carries no such
	// field, so the account a redemption may create is named nothing until
	// its owner sets one afterward. RedemptionRepository.Redeem ignores it
	// entirely when the account already exists (its own doc comment: "not a
	// licence for whoever issued the invitation to rename someone else's
	// account").
	result, err := s.redemptions.Redeem(r.Context(), strings.TrimSpace(req.Token), "")
	if err != nil {
		s.mapError(w, r, err)
		return
	}

	// The organisation's name is a courtesy read, not part of the
	// authorization: the redemption above already committed. A failure here
	// must not undo it or turn a successful redemption into an error the
	// invitee cannot explain — it simply means the response omits the name.
	org := ""
	if s.organizations != nil {
		if o, oerr := s.organizations.Get(r.Context(), result.Invitation.OrgID); oerr == nil {
			org = o.Name
		}
	}
	writeJSON(w, http.StatusOK, redeemInvitationResponse{Organization: org})
}
