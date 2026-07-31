//go:build loadtest

package main

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// mintToken signs an HS256 bearer token with the same claim shape the
// httpapi test suite uses (internal/httpapi/handlers_test.go: mintToken) —
// sub/iss/aud/exp/roles — and no tenant claim, which the server resolves to
// the system tenant (internal/httpapi/middleware.go: resolveTenant). A
// two-hour expiry comfortably outlives any run this harness would drive.
func mintToken(issuer, audience, secret, role string) (string, error) {
	claims := jwt.MapClaims{
		"sub":   "loadtest",
		"iss":   issuer,
		"aud":   audience,
		"exp":   time.Now().Add(2 * time.Hour).Unix(),
		"roles": []string{role},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString([]byte(secret))
}
