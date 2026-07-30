package domain

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

// InvitationStatus is the lifecycle of an invitation.
//
// 'expired' is a defined state but is never written by this package. Expiry is
// enforced by comparing expires_at at the moment of redemption, so an
// invitation that has run out of time is refused whether or not anything has
// swept it. Writing the status would be a cache of that comparison, and a cache
// that lags is a token that still works after it should not.
type InvitationStatus string

const (
	InvitationPending  InvitationStatus = "pending"
	InvitationRedeemed InvitationStatus = "redeemed"
	InvitationRevoked  InvitationStatus = "revoked"
	InvitationExpired  InvitationStatus = "expired"
)

// Valid reports whether s is a defined status.
func (s InvitationStatus) Valid() bool {
	switch s {
	case InvitationPending, InvitationRedeemed, InvitationRevoked, InvitationExpired:
		return true
	}
	return false
}

// invitationTokenBytes is the entropy of an invitation token.
//
// 32 bytes is the same budget as the webhook signing secret. The token is a
// bearer credential with no second factor and no rate limit in front of it at
// the database, so guessing must be infeasible rather than merely difficult.
const invitationTokenBytes = 32

// errRandomSource reports that the system's entropy source failed. There is no
// safe fallback: a predictable invitation token is a standing grant of access
// to an organisation.
var errRandomSource = errors.New("domain: random source failed")

// ErrTokenNotRedeemable reports that a token did not name a redeemable
// invitation.
//
// It is deliberately one error for four causes — no such token, already
// redeemed, revoked, expired. Distinguishing them would tell a caller holding a
// guessed token whether the guess existed, which turns the redeem endpoint into
// an oracle for enumerating valid invitations.
var ErrTokenNotRedeemable = errors.New("invitation is not redeemable")

// Invitation is a pending grant of membership in an Organisation
// (ADR-IDENTITY-002 §2.4).
//
// There is deliberately no field for the token. The plaintext exists only as
// the second return value of NewInvitation, and is unrecoverable from this
// struct and from the table alike — an invitation token is a bearer credential
// and gets the same treatment as an API key (ADR-IDENTITY-001 §9.2).
type Invitation struct {
	InvitationID string
	// TenantID is the RLS key. It is denormalised from the organisation rather
	// than joined, so isolation never depends on reading a mapping table
	// (ADR-IDENTITY-002 §6).
	TenantID  string
	OrgID     string
	Email     string
	TokenHash string
	Status    InvitationStatus
	ExpiresAt time.Time
	CreatedAt time.Time
	// RedeemedAt is nil until the invitation is consumed.
	RedeemedAt *time.Time
}

// HashInvitationToken maps a plaintext token to what the table stores.
//
// SHA-256 without a salt or work factor is correct here and would not be for a
// password: the input is 32 bytes of uniform randomness this package generated,
// so there is no dictionary to precompute and no low-entropy guess to slow
// down. A work factor would only slow redemption.
func HashInvitationToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// TokenMatches reports whether token is the one this invitation was issued
// with, compared in constant time.
//
// The comparison is on the hashes, so a timing signal would leak information
// about a hash rather than the token. It is constant-time anyway: the cost is
// nothing and the habit is what stops the next comparison — over something that
// does matter — from being written with ==.
func (i *Invitation) TokenMatches(token string) bool {
	return subtle.ConstantTimeCompare(
		[]byte(i.TokenHash), []byte(HashInvitationToken(token)),
	) == 1
}

// newInvitationToken returns a fresh bearer token.
//
// The encoding is base64url, not hex, and that is deliberate on two counts. The
// token travels in an invitation link, so it must survive a URL without
// escaping. And 32 bytes of base64url is 43 characters where a hex SHA-256
// digest is 64 — the two are not the same width, so a plaintext token written
// into the column named for its hash fails Validate instead of being stored as
// though it were a digest. Same-width encodings would make that check blind to
// exactly the mistake it exists to catch.
func newInvitationToken() (string, error) {
	b := make([]byte, invitationTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", errRandomSource
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Redeemable reports why this invitation cannot be consumed at the given
// instant, or nil if it can.
//
// The repository re-checks all of this in SQL. This is not the enforcement
// point — a check-then-act across two statements is a race, and the store's
// single conditional UPDATE is what actually makes redemption single-use. This
// exists so the rule is stated once in the language of the domain and can be
// tested without a database.
func (i *Invitation) Redeemable(now time.Time) error {
	if i.Status != InvitationPending {
		return ErrTokenNotRedeemable
	}
	if !now.Before(i.ExpiresAt) {
		return ErrTokenNotRedeemable
	}
	return nil
}

// Expired reports whether the invitation's window has closed.
func (i *Invitation) Expired(now time.Time) bool { return !now.Before(i.ExpiresAt) }

// Validate enforces the invariants the database also enforces, so a bad request
// fails with a field-level 422 rather than a constraint-violation 500.
func (i *Invitation) Validate() error {
	if strings.TrimSpace(i.InvitationID) == "" {
		return newValidation("invitation_id", "must not be empty")
	}
	if strings.TrimSpace(i.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty")
	}
	if strings.TrimSpace(i.OrgID) == "" {
		return newValidation("org_id", "must not be empty")
	}
	if !emailPattern.MatchString(i.Email) {
		return newValidation("email", "must be a valid email address")
	}
	// A hash of the wrong width is not a hash of a token this package issued.
	// Accepting one would mean accepting a value some other path invented,
	// which is how a plaintext token ends up in the column named for its hash.
	if len(i.TokenHash) != sha256.Size*2 {
		return newValidation("token_hash", "must be a SHA-256 digest in hex")
	}
	if _, err := hex.DecodeString(i.TokenHash); err != nil {
		return newValidation("token_hash", "must be a SHA-256 digest in hex")
	}
	if !i.Status.Valid() {
		return newValidation("status", "must be one of: pending, redeemed, revoked, expired")
	}
	if i.ExpiresAt.IsZero() {
		return newValidation("expires_at", "must be set; an invitation without an expiry never stops working")
	}
	return nil
}

// NewInvitation mints an invitation and returns the plaintext token exactly
// once.
//
// The token is the second return value and is never stored on the struct. A
// caller that does not deliver it here cannot obtain it later, which is the
// property the table's `token_hash` column exists to guarantee — and the reason
// re-sending an invitation must issue a new one rather than recover the old.
//
// ttl is required rather than defaulted. An invitation is a standing grant of
// access to an organisation; the caller that decides to extend one decides for
// how long, and a package-level default would silently outlive the decision.
func NewInvitation(orgID, tenantID, email string, ttl time.Duration, now time.Time) (*Invitation, string, error) {
	if ttl <= 0 {
		return nil, "", newValidation("ttl", "must be positive; an invitation that has already "+
			"expired at issuance is indistinguishable from one that was never sent")
	}

	token, err := newInvitationToken()
	if err != nil {
		return nil, "", err
	}

	i := &Invitation{
		InvitationID: NewID(),
		TenantID:     strings.TrimSpace(tenantID),
		OrgID:        strings.TrimSpace(orgID),
		// The address is normalised the same way app_user normalises it, so an
		// invitation and the account it becomes agree on who was invited.
		Email:     NormalizeEmail(email),
		TokenHash: HashInvitationToken(token),
		Status:    InvitationPending,
		ExpiresAt: now.Add(ttl).UTC(),
	}
	if err := i.Validate(); err != nil {
		return nil, "", err
	}
	return i, token, nil
}

// InvitationRepository persists invitations.
//
// invitation is tenant-scoped: every method here except Consume runs under
// row-level security and sees only the caller's boundary (ADR-IDENTITY-002
// §2.4). Consume is the exception and says so in its own contract.
type InvitationRepository interface {
	Create(ctx context.Context, i *Invitation) (*Invitation, error)
	Get(ctx context.Context, invitationID string) (*Invitation, error)
	// ListByOrg returns invitations for one organisation, newest first.
	ListByOrg(ctx context.Context, orgID string, limit int, after string) ([]*Invitation, error)
	// Consume atomically redeems a pending, unexpired invitation and returns
	// it. It is single-use: the second call with the same token finds nothing
	// to redeem and reports ErrTokenNotRedeemable, as does a token that never
	// existed.
	//
	// It resolves an invitation BEFORE any tenant is known — the invitee holds
	// no membership yet, which is the fact a tenant would be resolved from —
	// so it necessarily runs outside row-level security, exactly as tenant
	// resolution does (ADR-TENANCY-001 §4). What authorises the read is
	// possession of 32 bytes of unguessable entropy.
	Consume(ctx context.Context, token string) (*Invitation, error)
	// Revoke withdraws a pending invitation. Revocation is a status change and
	// never a delete (ADR-IDENTITY-001 §8.3).
	Revoke(ctx context.Context, invitationID string) (*Invitation, error)
}
