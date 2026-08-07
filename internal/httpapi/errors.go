package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/rpsg/oneops/internal/domain"
)

// Problem is an RFC 7807 problem detail.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{
		Type:     "about:blank",
		Title:    title,
		Status:   status,
		Detail:   detail,
		Instance: RequestIDFrom(r.Context()),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// mapError translates a domain error into an RFC 7807 response.
//
// It is a method on Server so that the single place which decides an error has
// become a 500 can also record WHY. Before this, an unrecognized error was
// discarded here: the client received "unexpected error" and the operator saw
// only an INFO http_request line with status 500 and no cause. That is how the
// v1.0.0 nil-slice defect stayed invisible for a full release (PM-001).
//
// Only the unexpected branch logs. A 404, 409, 412 or 422 is a normal outcome,
// not a failure, and logging those at ERROR would train operators to ignore the
// channel — the noise failure mode is as real as the silence one.
func (s *Server) mapError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", err.Error())
	case errors.Is(err, domain.ErrConflict):
		writeProblem(w, r, http.StatusConflict, "conflict", "artifact/version already exists")
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusPreconditionFailed, "precondition failed", "row version mismatch")
	case errors.Is(err, domain.ErrInvalidTransition):
		// The target state is well-formed; it is the move that is refused. That
		// is a conflict with the record's current state, not a malformed request
		// — and without this case it would fall through to the 500 branch and be
		// logged as an unhandled server fault.
		writeProblem(w, r, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, domain.ErrDeletionForbidden):
		// §8: the role may never be deleted. A refusal on constitutional grounds,
		// not a client error in the request itself.
		writeProblem(w, r, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, domain.ErrTokenNotRedeemable):
		// One shape for every cause — unknown token, already redeemed, revoked,
		// expired, or naming a suspended/deactivated account (see
		// domain.ErrTokenNotRedeemable's own doc comment and
		// RedemptionRepository.Redeem). err.Error() is always the same sentinel
		// text; never replace it with anything that could vary by cause.
		writeProblem(w, r, http.StatusBadRequest, "invitation not redeemable", err.Error())
	default:
		if ve, ok := domain.AsValidation(err); ok {
			writeProblem(w, r, http.StatusUnprocessableEntity, "validation failed", ve.Error())
			return
		}
		// The cause goes to the log and never to the response body, which stays
		// deliberately opaque: the detail can carry internals (SQLSTATE, driver
		// text, table names). request_id correlates the two.
		s.log.Error("unhandled request error",
			"method", r.Method,
			"path", r.URL.Path,
			"request_id", RequestIDFrom(r.Context()),
			"err", err.Error())
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "unexpected error")
	}
}
