package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testTTL = 48 * time.Hour

func mustInvitation(t *testing.T) (*Invitation, string) {
	t.Helper()
	i, token, err := NewInvitation("org_1", "tn-1", "Invitee@Example.COM", testTTL, time.Now())
	if err != nil {
		t.Fatalf("NewInvitation: %v", err)
	}
	return i, token
}

// The acceptance criterion, at the level of the type: there must be no way to
// read the plaintext back out of the aggregate. A field added later that held
// it — for convenience, for a resend feature — would defeat the table's whole
// design, so the struct's shape is asserted rather than assumed.
func TestInvitation_StructHoldsNoPlaintextToken(t *testing.T) {
	i, token := mustInvitation(t)

	v := reflect.ValueOf(*i)
	for f := 0; f < v.NumField(); f++ {
		name := v.Type().Field(f).Name
		if s, ok := v.Field(f).Interface().(string); ok && s == token {
			t.Fatalf("field %s holds the plaintext token; it must exist only as the second "+
				"return value of NewInvitation", name)
		}
		if strings.Contains(strings.ToLower(name), "token") && name != "TokenHash" {
			t.Errorf("field %s may hold token material; only TokenHash may exist", name)
		}
	}
}

func TestNewInvitation_IssuesAHashNotTheToken(t *testing.T) {
	i, token := mustInvitation(t)

	if i.TokenHash == token {
		t.Fatal("token_hash is the plaintext token")
	}
	if i.TokenHash != HashInvitationToken(token) {
		t.Error("token_hash is not the hash of the issued token")
	}
	if len(i.TokenHash) != sha256.Size*2 {
		t.Errorf("token_hash is %d chars, want %d", len(i.TokenHash), sha256.Size*2)
	}
	if _, err := hex.DecodeString(i.TokenHash); err != nil {
		t.Errorf("token_hash is not hex: %v", err)
	}
}

// A token short enough to guess is not a credential. 32 bytes is the budget the
// package documents; a change to it should have to change this too.
func TestNewInvitation_TokenCarriesFullEntropy(t *testing.T) {
	_, token := mustInvitation(t)

	// An absolute floor, not a restatement of the constant. Deriving the whole
	// expectation from invitationTokenBytes makes the test agree with any value
	// it is given — a budget cut to 4 bytes passed. 32 bytes is the documented
	// minimum for an unguessable bearer credential; lowering it must fail here.
	if invitationTokenBytes < 32 {
		t.Fatalf("invitationTokenBytes is %d; an invitation token is an unrate-limited bearer "+
			"credential and needs at least 32 bytes of entropy", invitationTokenBytes)
	}
	if len(token) < 43 {
		t.Fatalf("token is %d chars; 32 bytes of base64url is 43", len(token))
	}

	// base64url, so the token is URL-safe and is NOT the same width as the hex
	// digest stored for it — see newInvitationToken.
	if want := base64.RawURLEncoding.EncodedLen(invitationTokenBytes); len(token) != want {
		t.Fatalf("token is %d chars, want %d (%d bytes base64url)", len(token), want, invitationTokenBytes)
	}
	if len(token) == sha256.Size*2 {
		t.Fatal("the token is the same width as its hash; a plaintext token written into " +
			"token_hash would then pass Validate")
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		t.Fatalf("token is not base64url: %v", err)
	}
	if len(raw) != invitationTokenBytes {
		t.Fatalf("token decodes to %d bytes, want %d", len(raw), invitationTokenBytes)
	}
	// All-zero is what a silently failing entropy source produces.
	zero := true
	for _, b := range raw {
		if b != 0 {
			zero = false
			break
		}
	}
	if zero {
		t.Fatal("token is all zero bytes; the entropy source did not fill it")
	}
}

// Two invitations must never share a token. Issuance is the only place a token
// is created, so a deterministic mint would grant every invitee the same access.
func TestNewInvitation_TokensAreUnique(t *testing.T) {
	seen := map[string]bool{}
	hashes := map[string]bool{}
	for n := 0; n < 200; n++ {
		i, token := mustInvitation(t)
		if seen[token] {
			t.Fatalf("token repeated after %d issuances", n)
		}
		if hashes[i.TokenHash] {
			t.Fatalf("token_hash repeated after %d issuances", n)
		}
		seen[token], hashes[i.TokenHash] = true, true
	}
}

func TestNewInvitation_NormalisesAndSetsDefaults(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	i, _, err := NewInvitation(" org_1 ", " tn-1 ", "  Invitee@Example.COM  ", testTTL, now)
	if err != nil {
		t.Fatalf("NewInvitation: %v", err)
	}

	if i.Email != "invitee@example.com" {
		t.Errorf("email %q: want the same normalisation app_user applies", i.Email)
	}
	if i.OrgID != "org_1" || i.TenantID != "tn-1" {
		t.Errorf("identifiers not trimmed: org=%q tenant=%q", i.OrgID, i.TenantID)
	}
	if i.Status != InvitationPending {
		t.Errorf("status %q, want pending", i.Status)
	}
	if !i.ExpiresAt.Equal(now.Add(testTTL)) {
		t.Errorf("expires_at %v, want %v", i.ExpiresAt, now.Add(testTTL))
	}
	if i.RedeemedAt != nil {
		t.Error("a new invitation must not be redeemed")
	}
	if i.InvitationID == "" {
		t.Error("invitation_id must be minted")
	}
}

// An invitation with no expiry, or one already expired at issuance, is not a
// time-bounded grant. Both are refused at construction.
func TestNewInvitation_RejectsNonPositiveTTL(t *testing.T) {
	for _, ttl := range []time.Duration{0, -time.Second, -testTTL} {
		if _, _, err := NewInvitation("org_1", "tn-1", "a@b.com", ttl, time.Now()); err == nil {
			t.Errorf("ttl=%v was accepted; an invitation must expire in the future", ttl)
		}
	}
}

func TestNewInvitation_RejectsInvalidInput(t *testing.T) {
	for _, c := range []struct{ name, org, tenant, email string }{
		{"no org", "", "tn-1", "a@b.com"},
		{"no tenant", "org_1", "", "a@b.com"},
		{"no email", "org_1", "tn-1", ""},
		{"malformed email", "org_1", "tn-1", "not-an-email"},
		{"email without domain", "org_1", "tn-1", "a@b"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := NewInvitation(c.org, c.tenant, c.email, testTTL, time.Now()); err == nil {
				t.Error("invalid input was accepted")
			}
		})
	}
}

// token_hash must be a digest. A plaintext token written into the column named
// for its hash is exactly the failure the column exists to prevent, and it is
// the width check that catches it.
func TestInvitation_ValidateRejectsNonDigestTokenHash(t *testing.T) {
	i, token := mustInvitation(t)

	for _, c := range []struct{ name, hash string }{
		{"plaintext token", token},
		{"empty", ""},
		{"too short", strings.Repeat("a", 63)},
		{"too long", strings.Repeat("a", 65)},
		{"right width, not hex", strings.Repeat("z", 64)},
	} {
		t.Run(c.name, func(t *testing.T) {
			cp := *i
			cp.TokenHash = c.hash
			if err := cp.Validate(); err == nil {
				t.Errorf("token_hash %q was accepted", c.hash)
			}
		})
	}
}

func TestInvitation_TokenMatches(t *testing.T) {
	i, token := mustInvitation(t)

	if !i.TokenMatches(token) {
		t.Error("the issued token does not match its own invitation")
	}
	_, other := mustInvitation(t)
	if i.TokenMatches(other) {
		t.Error("another invitation's token matched")
	}
	for _, wrong := range []string{"", token + "0", token[:len(token)-1], strings.ToUpper(token)} {
		if i.TokenMatches(wrong) {
			t.Errorf("token %q matched", wrong)
		}
	}
}

func TestHashInvitationToken_IsStableAndDistinct(t *testing.T) {
	// Assigned first: staticcheck rejects comparing two literally identical
	// expressions, and the property under test is that repeated calls agree.
	first, again := HashInvitationToken("abc"), HashInvitationToken("abc")
	if first != again {
		t.Error("hashing is not deterministic")
	}
	if first == HashInvitationToken("abd") {
		t.Error("distinct tokens hashed to the same digest")
	}
	// Pinned against a known SHA-256 so a change of algorithm is visible rather
	// than silently invalidating every stored hash.
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	if got := HashInvitationToken("abc"); got != want {
		t.Errorf("HashInvitationToken(\"abc\") = %s, want the SHA-256 digest %s", got, want)
	}
}

func TestInvitation_Redeemable(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	base, _ := mustInvitation(t)
	base.ExpiresAt = now.Add(time.Hour)

	if err := base.Redeemable(now); err != nil {
		t.Errorf("a pending, unexpired invitation is not redeemable: %v", err)
	}

	for _, st := range []InvitationStatus{InvitationRedeemed, InvitationRevoked, InvitationExpired} {
		cp := *base
		cp.Status = st
		if err := cp.Redeemable(now); !errors.Is(err, ErrTokenNotRedeemable) {
			t.Errorf("status %q: got %v, want ErrTokenNotRedeemable", st, err)
		}
	}

	// Expiry is exclusive: at exactly expires_at the window has closed. A token
	// valid at its own expiry instant is a token valid for one moment longer
	// than the caller was told.
	for _, at := range []time.Time{base.ExpiresAt, base.ExpiresAt.Add(time.Nanosecond), base.ExpiresAt.Add(time.Hour)} {
		if err := base.Redeemable(at); !errors.Is(err, ErrTokenNotRedeemable) {
			t.Errorf("at %v (expires %v): got %v, want ErrTokenNotRedeemable", at, base.ExpiresAt, err)
		}
	}
	if !base.Expired(base.ExpiresAt) {
		t.Error("Expired must be true at exactly expires_at")
	}
	if base.Expired(base.ExpiresAt.Add(-time.Nanosecond)) {
		t.Error("Expired must be false just before expires_at")
	}
}

func TestInvitationStatus_Valid(t *testing.T) {
	for _, st := range []InvitationStatus{
		InvitationPending, InvitationRedeemed, InvitationRevoked, InvitationExpired,
	} {
		if !st.Valid() {
			t.Errorf("%q must be valid", st)
		}
	}
	for _, st := range []InvitationStatus{"", "PENDING", "deleted", "active", "invited"} {
		if InvitationStatus(st).Valid() {
			t.Errorf("%q must not be valid", st)
		}
	}
}
