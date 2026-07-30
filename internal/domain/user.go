package domain

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

// UserStatus is the lifecycle of a user (ADR-IDENTITY-001 §8.3).
//
// Deactivation is terminal and revokes every membership, but the row survives:
// audit events name their author, and an author that could be deleted would
// leave the append-only chain attributing acts to nobody. Removal is therefore
// not modelled here, exactly as it is not modelled for Tenant.
type UserStatus string

const (
	// UserInvited is a user who exists because someone invited them and who has
	// not yet accepted. They hold no access.
	UserInvited UserStatus = "invited"
	// UserActive is a user who may hold memberships and act through them.
	UserActive UserStatus = "active"
	// UserSuspended is a reversible removal of access. Memberships are retained.
	UserSuspended UserStatus = "suspended"
	// UserDeactivated is terminal. The row is kept for audit attribution.
	UserDeactivated UserStatus = "deactivated"
)

// Valid reports whether s is a defined status.
func (s UserStatus) Valid() bool {
	switch s {
	case UserInvited, UserActive, UserSuspended, UserDeactivated:
		return true
	}
	return false
}

// userTransitions is the whole lifecycle, as data rather than as a chain of
// conditionals. A transition absent here is refused, so adding a state means
// stating every edge it has instead of discovering the omissions in production.
//
// deactivated has no outgoing edges: it is terminal by ADR-IDENTITY-001 §8.3.
var userTransitions = map[UserStatus][]UserStatus{
	UserInvited:     {UserActive, UserDeactivated},
	UserActive:      {UserSuspended, UserDeactivated},
	UserSuspended:   {UserActive, UserDeactivated},
	UserDeactivated: {},
}

// CanTransitionTo reports whether from -> to is a permitted lifecycle move.
// A transition to the current state is refused: it is a no-op that would
// consume a row version and write an audit event recording nothing.
func (s UserStatus) CanTransitionTo(to UserStatus) bool {
	for _, allowed := range userTransitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// emailPattern is deliberately permissive. Full RFC 5322 validation rejects
// addresses that deliver, and accepts addresses that do not; the only proof an
// address works is a message arriving at it. This rejects the shapes that are
// certainly wrong — no @, nothing before or after it, whitespace, no dot in the
// domain — and leaves the rest to the invitation the address must redeem.
var emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

// MaxEmailLength bounds storage and log lines. 254 is the SMTP path limit.
const MaxEmailLength = 254

// MaxDisplayNameLength bounds a free-text field that reaches consoles and logs.
const MaxDisplayNameLength = 200

// User is a person-shaped Identity, global to the platform.
//
// It carries NO tenant: a person exists before, and independently of, any
// membership, and the same human may belong to several organisations
// (ADR-IDENTITY-001 §8.2). Membership is what scopes a user to an organisation,
// and tenant_id remains the single authoritative ownership key for every row
// that is owned at all (ADR-IDENTITY-001 §4.2).
type User struct {
	UserID string
	// Email is unique platform-wide and compared case-insensitively. The column
	// is citext, so uniqueness holds regardless of the casing supplied.
	Email       string
	DisplayName string
	Status      UserStatus
	RowVersion  int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Active reports whether the user may hold and exercise memberships.
func (u *User) Active() bool { return u.Status == UserActive }

// Validate enforces the invariants the database also enforces, so a bad request
// fails with a field-level 422 rather than a constraint-violation 500.
func (u *User) Validate() error {
	if strings.TrimSpace(u.UserID) == "" {
		return newValidation("user_id", "must not be empty")
	}
	email := strings.TrimSpace(u.Email)
	if email == "" {
		return newValidation("email", "must not be empty")
	}
	if len(email) > MaxEmailLength {
		return newValidation("email", "must be at most 254 characters")
	}
	if !emailPattern.MatchString(email) {
		return newValidation("email", "must be a valid email address")
	}
	if len(u.DisplayName) > MaxDisplayNameLength {
		return newValidation("display_name", "must be at most 200 characters")
	}
	if !u.Status.Valid() {
		return newValidation("status", "must be one of: invited, active, suspended, deactivated")
	}
	return nil
}

// NormalizeEmail trims surrounding whitespace and lowercases the address.
//
// The column is citext, so uniqueness does not depend on this. Normalising on
// the way in means the stored value is also what gets logged, compared in Go,
// and shown back to the user — and that a lookup written without the citext
// operator in scope (see the note on invitation.email) still matches.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// NewUser builds an invited user with a platform-generated identity.
//
// The id is minted here and never accepted from a caller: a client-supplied
// identifier on a globally unique table is the cross-tenant key-confusion
// defect the Trust Register records as entry 1.
func NewUser(email, displayName string) (*User, error) {
	u := &User{
		UserID:      NewID(),
		Email:       NormalizeEmail(email),
		DisplayName: strings.TrimSpace(displayName),
		Status:      UserInvited,
	}
	if err := u.Validate(); err != nil {
		return nil, err
	}
	return u, nil
}

// UserRepository persists users. Users are global: no method takes a tenant,
// and none may be scoped by one (ADR-IDENTITY-002 §3).
type UserRepository interface {
	Create(ctx context.Context, u *User) (*User, error)
	Get(ctx context.Context, userID string) (*User, error)
	// GetByEmail resolves an address case-insensitively. It returns ErrNotFound
	// for an unknown address; callers on an unauthenticated path must not
	// distinguish that from a known one in what they return (ADR-IDENTITY-002
	// AC-10).
	GetByEmail(ctx context.Context, email string) (*User, error)
	List(ctx context.Context, limit int, after string) ([]*User, error)
	// UpdateProfile changes the mutable descriptive fields, guarded by
	// rowVersion. It cannot change status: lifecycle moves go through
	// SetStatus, which is the only place transitions are checked.
	UpdateProfile(ctx context.Context, userID string, rowVersion int64, displayName string) (*User, error)
	// SetStatus performs a lifecycle transition, guarded by rowVersion. It
	// refuses a move that UserStatus.CanTransitionTo rejects.
	SetStatus(ctx context.Context, userID string, rowVersion int64, status UserStatus) (*User, error)
}

// ErrInvalidTransition indicates a lifecycle move the state machine forbids —
// most often reviving a deactivated user, which is terminal.
//
// It is distinct from ValidationError because the target state is well-formed:
// it is the move that is refused, so transport maps it to 409 rather than 422.
var ErrInvalidTransition = errors.New("invalid lifecycle transition")

// TransitionError is a refused lifecycle move, naming both ends so the message
// says which move was attempted rather than only that one was. It wraps
// ErrInvalidTransition, so callers may match the class with errors.Is and still
// read the detail.
type TransitionError struct {
	From UserStatus
	To   UserStatus
}

func (e *TransitionError) Error() string {
	return "invalid lifecycle transition: " + string(e.From) + " -> " + string(e.To)
}

// Unwrap makes errors.Is(err, ErrInvalidTransition) true for this type.
func (e *TransitionError) Unwrap() error { return ErrInvalidTransition }

// NewTransitionError builds a *TransitionError naming both ends of the move.
func NewTransitionError(from, to UserStatus) *TransitionError {
	return &TransitionError{From: from, To: to}
}
