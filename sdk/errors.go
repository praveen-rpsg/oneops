package sdk

import (
	"errors"
	"fmt"
	"net/http"
)

// APIError is a decoded RFC 7807 problem+json response from the platform. It is
// returned for any non-2xx status; the classification helpers (IsNotFound, etc.)
// branch on Status without string matching.
type APIError struct {
	// Status is the HTTP status code.
	Status int
	// Title is the problem title.
	Title string
	// Detail is the human-readable detail, if any.
	Detail string
	// RequestID echoes the correlation id (problem "instance"), if present.
	RequestID string
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("oneops: %d %s: %s", e.Status, e.Title, e.Detail)
	}
	return fmt.Sprintf("oneops: %d %s", e.Status, e.Title)
}

// asAPIError extracts an *APIError from err, if present.
func asAPIError(err error) (*APIError, bool) {
	var e *APIError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsValidation reports a request/validation failure (400 or 422).
func IsValidation(err error) bool {
	return statusIs(err, http.StatusBadRequest, http.StatusUnprocessableEntity)
}

// IsUnauthorized reports a missing/invalid credential (401).
func IsUnauthorized(err error) bool { return statusIs(err, http.StatusUnauthorized) }

// IsForbidden reports insufficient permission (403).
func IsForbidden(err error) bool { return statusIs(err, http.StatusForbidden) }

// IsNotFound reports an unknown resource (404).
func IsNotFound(err error) bool { return statusIs(err, http.StatusNotFound) }

// IsConflict reports a state or concurrency conflict (409 or 412 version mismatch).
func IsConflict(err error) bool {
	return statusIs(err, http.StatusConflict, http.StatusPreconditionFailed)
}

// IsServerError reports a server-side failure (5xx).
func IsServerError(err error) bool {
	e, ok := asAPIError(err)
	return ok && e.Status >= 500
}

// IsRetryable reports whether err represents a transient condition the SDK would
// retry for an idempotent request (network/timeout error, 429, or 5xx).
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := asAPIError(err); ok {
		return e.Status == http.StatusTooManyRequests || e.Status >= 500
	}
	// A non-API error reaching the caller is a transport/network error.
	return true
}

func statusIs(err error, codes ...int) bool {
	e, ok := asAPIError(err)
	if !ok {
		return false
	}
	for _, c := range codes {
		if e.Status == c {
			return true
		}
	}
	return false
}
