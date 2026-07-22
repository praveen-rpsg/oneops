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
func mapError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, r, http.StatusNotFound, "not found", err.Error())
	case errors.Is(err, domain.ErrConflict):
		writeProblem(w, r, http.StatusConflict, "conflict", "artifact/version already exists")
	case errors.Is(err, domain.ErrVersionMismatch):
		writeProblem(w, r, http.StatusPreconditionFailed, "precondition failed", "row version mismatch")
	default:
		if ve, ok := domain.AsValidation(err); ok {
			writeProblem(w, r, http.StatusUnprocessableEntity, "validation failed", ve.Error())
			return
		}
		writeProblem(w, r, http.StatusInternalServerError, "internal error", "unexpected error")
	}
}
