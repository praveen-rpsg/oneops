package auth

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	testSecret = "unit-test-secret"
	testIss    = "https://oneops.local"
	testAud    = "oneops"
)

func mint(t *testing.T, mutate func(jwt.MapClaims)) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":    "user-1",
		"iss":    testIss,
		"aud":    testAud,
		"exp":    time.Now().Add(time.Hour).Unix(),
		"roles":  []string{"oneops-admin"},
		"tenant": "tenant-a",
	}
	if mutate != nil {
		mutate(claims)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return s
}

func testVerifier() *Verifier {
	return NewVerifier(testIss, testAud, testSecret, "")
}

func TestVerifyOK(t *testing.T) {
	claims, err := testVerifier().Verify(mint(t, nil))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.Subject != "user-1" {
		t.Errorf("subject = %q", claims.Subject)
	}
	if claims.Tenant != "tenant-a" {
		t.Errorf("tenant = %q", claims.Tenant)
	}
	if len(claims.Roles) != 1 || claims.Roles[0] != "oneops-admin" {
		t.Errorf("roles = %v", claims.Roles)
	}
}

func TestVerifyRejections(t *testing.T) {
	v := testVerifier()

	if _, err := v.Verify(mint(t, func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Hour).Unix() })); err == nil {
		t.Error("expected expired token rejection")
	}
	if _, err := v.Verify(mint(t, func(c jwt.MapClaims) { c["iss"] = "https://evil" })); err == nil {
		t.Error("expected issuer rejection")
	}
	if _, err := v.Verify(mint(t, func(c jwt.MapClaims) { c["aud"] = "other" })); err == nil {
		t.Error("expected audience rejection")
	}
	if _, err := v.Verify(mint(t, func(c jwt.MapClaims) { delete(c, "sub") })); err == nil {
		t.Error("expected missing-subject rejection")
	}

	// Wrong signing key.
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "x", "iss": testIss, "aud": testAud, "exp": time.Now().Add(time.Hour).Unix(),
	})
	bad, _ := tok.SignedString([]byte("wrong-secret"))
	if _, err := v.Verify(bad); err == nil {
		t.Error("expected signature rejection")
	}

	if _, err := v.Verify("not-a-token"); err == nil {
		t.Error("expected malformed rejection")
	}
}

func TestParseRoles(t *testing.T) {
	if got := parseRoles([]any{"a", "b", 3}); len(got) != 2 {
		t.Errorf("parseRoles []any = %v", got)
	}
	if got := parseRoles([]string{"a"}); len(got) != 1 {
		t.Errorf("parseRoles []string = %v", got)
	}
	if got := parseRoles("solo"); len(got) != 1 {
		t.Errorf("parseRoles string = %v", got)
	}
	if got := parseRoles(nil); got != nil {
		t.Errorf("parseRoles nil = %v", got)
	}
}
