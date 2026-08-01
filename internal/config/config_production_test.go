package config

import (
	"strings"
	"testing"
)

func TestValidateProduction_RejectsInsecureDefaults(t *testing.T) {
	c := &Config{
		Env:         "production",
		AuthEnabled: true,
		JWTHMACKey:  devJWTHMACKey,
		DatabaseURL: devDatabaseURL,
	}
	err := c.validateProduction()
	if err == nil {
		t.Fatal("expected production validation to reject insecure defaults")
	}
	for _, want := range []string{"ONEOPS_JWT_HMAC_KEY", "ONEOPS_DB_URL", "sslmode=disable"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing mention of %q", err.Error(), want)
		}
	}
}

func TestValidateProduction_RejectsAuthDisabled(t *testing.T) {
	c := &Config{
		Env:         "prod",
		AuthEnabled: false,
		DatabaseURL: "postgres://u:p@db:5432/oneops?sslmode=require",
	}
	if err := c.validateProduction(); err == nil {
		t.Fatal("expected production to reject auth disabled")
	}
}

func TestValidateProduction_AllowsSecureProd(t *testing.T) {
	c := &Config{
		Env:         "production",
		AuthEnabled: true,
		JWTHMACKey:  "a-real-strong-secret",
		DatabaseURL: "postgres://u:p@db.internal:5432/oneops?sslmode=require",
		MetricsAddr: ":9090",
	}
	if err := c.validateProduction(); err != nil {
		t.Fatalf("secure production config rejected: %v", err)
	}
}

// /metrics on the public listener discloses audit-integrity state, per-route
// request volumes and dependency health to any unauthenticated caller. None of
// it is tenant data, but it tells an attacker when audit verification is
// failing. Production must bind observability separately.
func TestValidateProduction_RejectsMetricsOnPublicListener(t *testing.T) {
	c := &Config{
		Env:         "production",
		AuthEnabled: true,
		JWTHMACKey:  "a-real-strong-secret",
		DatabaseURL: "postgres://u:p@db.internal:5432/oneops?sslmode=require",
		MetricsAddr: "",
	}
	if err := c.validateProduction(); err == nil {
		t.Fatal("production must reject serving /metrics on the public listener")
	}
}

func TestValidateProduction_NoOpOutsideProd(t *testing.T) {
	c := &Config{
		Env:         "dev",
		AuthEnabled: true,
		JWTHMACKey:  devJWTHMACKey,
		DatabaseURL: devDatabaseURL,
	}
	if err := c.validateProduction(); err != nil {
		t.Fatalf("dev config must not be rejected: %v", err)
	}
	if c.IsProduction() {
		t.Fatal("dev must not be detected as production")
	}
}

// An additional IdP left on the shared insecure development HMAC key is the
// same defect as the default IdP being left on it — production must reject
// it the same way.
func TestValidateProduction_RejectsInsecureAdditionalIdP(t *testing.T) {
	c := &Config{
		Env:         "production",
		AuthEnabled: true,
		JWTHMACKey:  "a-real-strong-secret",
		DatabaseURL: "postgres://u:p@db.internal:5432/oneops?sslmode=require",
		MetricsAddr: ":9090",
		AdditionalIDPs: []IDPSpec{
			{Issuer: "https://idp-a.example", Audience: "aud-a", HMACKey: devJWTHMACKey},
		},
	}
	err := c.validateProduction()
	if err == nil {
		t.Fatal("expected production to reject an additional IdP on the insecure dev HMAC key")
	}
	if !strings.Contains(err.Error(), "ONEOPS_ADDITIONAL_IDPS") {
		t.Errorf("error %q missing mention of ONEOPS_ADDITIONAL_IDPS", err.Error())
	}
}

func TestIsProduction(t *testing.T) {
	for _, env := range []string{"prod", "production", "PROD", " Production "} {
		if !(&Config{Env: env}).IsProduction() {
			t.Errorf("Env %q should be production", env)
		}
	}
	for _, env := range []string{"dev", "staging", "test", ""} {
		if (&Config{Env: env}).IsProduction() {
			t.Errorf("Env %q should not be production", env)
		}
	}
}
