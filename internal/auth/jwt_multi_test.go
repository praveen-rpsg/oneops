package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// mintFor signs an HS256 token for an arbitrary issuer/audience/secret, for
// exercising multi-IdP selection (mint, in jwt_test.go, is pinned to the
// single default testIss/testAud/testSecret).
func mintFor(t *testing.T, iss, aud, secret string, mutate func(jwt.MapClaims)) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub": "user-1",
		"iss": iss,
		"aud": aud,
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	if mutate != nil {
		mutate(claims)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

const (
	idpAIssuer = "https://idp-a.example"
	idpAAud    = "aud-a"
	idpASecret = "secret-a"

	idpBIssuer = "https://idp-b.example"
	idpBAud    = "aud-b"
	idpBSecret = "secret-b"
)

func multiVerifier(t *testing.T) *Verifier {
	t.Helper()
	v, err := NewMultiVerifier([]IDPConfig{
		{Issuer: idpAIssuer, Audience: idpAAud, HMACKey: idpASecret},
		{Issuer: idpBIssuer, Audience: idpBAud, HMACKey: idpBSecret},
	})
	if err != nil {
		t.Fatalf("NewMultiVerifier: %v", err)
	}
	return v
}

func TestMultiIdPSelectsByIssuer(t *testing.T) {
	v := multiVerifier(t)

	claims, err := v.Verify(mintFor(t, idpAIssuer, idpAAud, idpASecret, nil))
	if err != nil {
		t.Fatalf("verify idp A: %v", err)
	}
	if claims.Subject != "user-1" {
		t.Errorf("subject = %q", claims.Subject)
	}

	if _, err := v.Verify(mintFor(t, idpBIssuer, idpBAud, idpBSecret, nil)); err != nil {
		t.Fatalf("verify idp B: %v", err)
	}
}

// Verify carries the VERIFIED issuer out in the claims, so a downstream
// issuer-to-tenant binding rests on a checked value (ADR-IDENTITY-003).
func TestVerifyCarriesVerifiedIssuer(t *testing.T) {
	v := multiVerifier(t)

	a, err := v.Verify(mintFor(t, idpAIssuer, idpAAud, idpASecret, nil))
	if err != nil {
		t.Fatalf("verify idp A: %v", err)
	}
	if a.Issuer != idpAIssuer {
		t.Errorf("claims.Issuer = %q, want %q", a.Issuer, idpAIssuer)
	}

	b, err := v.Verify(mintFor(t, idpBIssuer, idpBAud, idpBSecret, nil))
	if err != nil {
		t.Fatalf("verify idp B: %v", err)
	}
	if b.Issuer != idpBIssuer {
		t.Errorf("claims.Issuer = %q, want %q", b.Issuer, idpBIssuer)
	}
}

func TestMultiIdPRejectsCrossAudience(t *testing.T) {
	v := multiVerifier(t)

	// A token asserting idp A's issuer but idp B's audience must be rejected:
	// verification always uses the audience configured for the matched
	// issuer, never any other IdP's.
	if _, err := v.Verify(mintFor(t, idpAIssuer, idpBAud, idpASecret, nil)); err == nil {
		t.Error("expected rejection: idp A issuer with idp B audience")
	}
	if _, err := v.Verify(mintFor(t, idpBIssuer, idpAAud, idpBSecret, nil)); err == nil {
		t.Error("expected rejection: idp B issuer with idp A audience")
	}
}

func TestMultiIdPRejectsCrossSignature(t *testing.T) {
	v := multiVerifier(t)

	// idp A's issuer/audience but signed with idp B's secret: the issuer
	// selects idp A's key, which must not accept idp B's signature.
	if _, err := v.Verify(mintFor(t, idpAIssuer, idpAAud, idpBSecret, nil)); err == nil {
		t.Error("expected signature rejection across IdPs")
	}
}

func TestMultiIdPRejectsUnknownIssuer(t *testing.T) {
	v := multiVerifier(t)

	tok := mintFor(t, "https://not-configured.example", idpAAud, idpASecret, nil)
	if _, err := v.Verify(tok); err == nil {
		t.Error("expected rejection for an issuer with no configured IdP")
	}
}

func TestMultiIdPRejectsMissingIssuerClaim(t *testing.T) {
	v := multiVerifier(t)

	tok := mintFor(t, idpAIssuer, idpAAud, idpASecret, func(c jwt.MapClaims) { delete(c, "iss") })
	if _, err := v.Verify(tok); err == nil {
		t.Error("expected rejection for a token with no issuer claim")
	}
}

func TestNewMultiVerifierRejectsDuplicateIssuer(t *testing.T) {
	_, err := NewMultiVerifier([]IDPConfig{
		{Issuer: idpAIssuer, Audience: idpAAud, HMACKey: idpASecret},
		{Issuer: idpAIssuer, Audience: "other-aud", HMACKey: "other-secret"},
	})
	if err == nil {
		t.Error("expected duplicate-issuer rejection")
	}
}

func TestNewMultiVerifierRejectsMissingIssuer(t *testing.T) {
	_, err := NewMultiVerifier([]IDPConfig{{Audience: idpAAud, HMACKey: idpASecret}})
	if err == nil {
		t.Error("expected rejection of an IdP config with no issuer")
	}
}

// Single-IdP construction (NewVerifier) is the backward-compatible path: an
// existing deployment with one issuer, unaware multi-IdP exists, must keep
// verifying tokens exactly as before.
func TestSingleIdPBackwardCompat(t *testing.T) {
	v := NewVerifier(testIss, testAud, testSecret, "")
	claims, err := v.Verify(mint(t, nil))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "user-1" || claims.Tenant != "tenant-a" {
		t.Fatalf("unexpected claims: %+v", claims)
	}

	// Still rejects an issuer it never trusted.
	if _, err := v.Verify(mintFor(t, idpAIssuer, testAud, testSecret, nil)); err == nil {
		t.Error("expected rejection for an unconfigured issuer")
	}
}

// Per-IdP RS256 rotation: an unknown kid on IdP B must refetch IdP B's JWKS
// (and only IdP B's), reusing the same jwksCache rotation logic as the
// single-IdP path.
func TestMultiIdPRS256Rotation(t *testing.T) {
	keyA, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const kidA, kidB = "kid-a", "kid-b"
	srvA := jwksServer(t, kidA, &keyA.PublicKey)
	defer srvA.Close()
	srvB := jwksServer(t, kidB, &keyB.PublicKey)
	defer srvB.Close()

	v, err := NewMultiVerifier([]IDPConfig{
		{Issuer: idpAIssuer, Audience: idpAAud, JWKSURL: srvA.URL, Client: srvA.Client()},
		{Issuer: idpBIssuer, Audience: idpBAud, JWKSURL: srvB.URL, Client: srvB.Client()},
	})
	if err != nil {
		t.Fatalf("NewMultiVerifier: %v", err)
	}

	sign := func(iss, aud string, key *rsa.PrivateKey, kid string) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"sub": "rsa-user", "iss": iss, "aud": aud, "exp": time.Now().Add(time.Hour).Unix(),
		})
		tok.Header["kid"] = kid
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	if _, err := v.Verify(sign(idpAIssuer, idpAAud, keyA, kidA)); err != nil {
		t.Fatalf("verify idp A: %v", err)
	}
	if _, err := v.Verify(sign(idpBIssuer, idpBAud, keyB, kidB)); err != nil {
		t.Fatalf("verify idp B: %v", err)
	}

	// idp A must never accept idp B's key, even carrying idp A's issuer/aud.
	if _, err := v.Verify(sign(idpAIssuer, idpAAud, keyB, kidB)); err == nil {
		t.Error("expected idp A to reject a token signed by idp B's key")
	}

	// unknown-kid on idp A triggers a refetch of idp A's JWKS only.
	if _, err := v.Verify(sign(idpAIssuer, idpAAud, keyA, "rotated-kid-not-yet-published")); err == nil {
		t.Error("expected unknown-kid rejection")
	}
}
