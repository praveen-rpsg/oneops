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
	}
	if err := c.validateProduction(); err != nil {
		t.Fatalf("secure production config rejected: %v", err)
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
