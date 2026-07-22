package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func encodeE(e int) string {
	var b []byte
	for e > 0 {
		b = append([]byte{byte(e & 0xff)}, b...)
		e >>= 8
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func jwksServer(t *testing.T, kid string, pub *rsa.PublicKey) *httptest.Server {
	t.Helper()
	doc := map[string]any{"keys": []map[string]string{{
		"kid": kid, "kty": "RSA",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": encodeE(pub.E),
	}}}
	body, _ := json.Marshal(doc)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
}

func TestVerifyRS256ViaJWKS(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const kid = "test-kid"
	srv := jwksServer(t, kid, &key.PublicKey)
	defer srv.Close()

	v := NewVerifier(testIss, testAud, "", srv.URL)

	sign := func(k string) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"sub": "rsa-user", "iss": testIss, "aud": testAud,
			"exp": time.Now().Add(time.Hour).Unix(), "roles": []any{"oneops-reader"},
		})
		tok.Header["kid"] = k
		s, err := tok.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	claims, err := v.Verify(sign(kid))
	if err != nil {
		t.Fatalf("verify RS256: %v", err)
	}
	if claims.Subject != "rsa-user" || len(claims.Roles) != 1 {
		t.Fatalf("unexpected claims: %+v", claims)
	}

	if _, err := v.Verify(sign("unknown-kid")); err == nil {
		t.Error("expected unknown-kid rejection")
	}

	// A verifier that accepts only RS256 must reject HS256 tokens.
	hs := mint(t, nil)
	if _, err := v.Verify(hs); err == nil {
		t.Error("expected HS256 rejection when only JWKS configured")
	}
}

func TestJWKSFetchError(t *testing.T) {
	v := NewVerifier(testIss, testAud, "", "http://127.0.0.1:0/jwks")
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"sub": "x", "iss": testIss, "aud": testAud, "exp": time.Now().Add(time.Hour).Unix(),
	})
	tok.Header["kid"] = "k"
	s, _ := tok.SignedString(mustRSA(t))
	if _, err := v.Verify(s); err == nil {
		t.Error("expected fetch error to fail verification")
	}
}

func mustRSA(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
