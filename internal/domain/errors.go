package domain

import (
	"errors"
	"fmt"
)

// Sentinel domain errors. Transport layers map these to HTTP status codes.
var (
	// ErrNotFound indicates the requested object does not exist.
	ErrNotFound = errors.New("configuration object not found")
	// ErrConflict indicates a uniqueness or version conflict.
	ErrConflict = errors.New("configuration object conflict")
	// ErrVersionMismatch indicates an optimistic-locking failure.
	ErrVersionMismatch = errors.New("row version mismatch")
)

// ValidationError describes an invalid field on an entity or request.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation failed on %q: %s", e.Field, e.Message)
}

// newValidation builds a *ValidationError.
func newValidation(field, msg string) *ValidationError {
	return &ValidationError{Field: field, Message: msg}
}

// NewValidationError builds a *ValidationError for use by transport layers.
func NewValidationError(field, msg string) *ValidationError {
	return &ValidationError{Field: field, Message: msg}
}

// AsValidation returns the *ValidationError if err is (or wraps) one.
func AsValidation(err error) (*ValidationError, bool) {
	var v *ValidationError
	if errors.As(err, &v) {
		return v, true
	}
	return nil, false
}
