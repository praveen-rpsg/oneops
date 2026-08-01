package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	t.Setenv("ONEOPS_HTTP_ADDR", "")
	t.Setenv("ONEOPS_ENV", "")
	t.Setenv("ONEOPS_SHUTDOWN_GRACE_SECONDS", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want %q", c.HTTPAddr, ":8080")
	}
	if c.Env != "dev" {
		t.Errorf("Env = %q, want %q", c.Env, "dev")
	}
	if c.ShutdownGrace != 10 {
		t.Errorf("ShutdownGrace = %d, want %d", c.ShutdownGrace, 10)
	}
}

func TestLoadOverride(t *testing.T) {
	t.Setenv("ONEOPS_HTTP_ADDR", ":9090")
	t.Setenv("ONEOPS_ENV", "prod")
	t.Setenv("ONEOPS_SHUTDOWN_GRACE_SECONDS", "30")
	// A production environment must supply secure values (production guard).
	t.Setenv("ONEOPS_JWT_HMAC_KEY", "a-real-strong-secret")
	t.Setenv("ONEOPS_DB_URL", "postgres://u:p@db.internal:5432/oneops?sslmode=require")

	c, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.HTTPAddr != ":9090" {
		t.Errorf("HTTPAddr = %q, want %q", c.HTTPAddr, ":9090")
	}
	if c.Env != "prod" {
		t.Errorf("Env = %q, want %q", c.Env, "prod")
	}
	if c.ShutdownGrace != 30 {
		t.Errorf("ShutdownGrace = %d, want %d", c.ShutdownGrace, 30)
	}
}

func TestLoadInvalidGrace(t *testing.T) {
	t.Setenv("ONEOPS_SHUTDOWN_GRACE_SECONDS", "-1")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for negative shutdown grace, got nil")
	}
}

func TestLoadDefaultsEnableRateLimiting(t *testing.T) {
	t.Setenv("ONEOPS_RATE_LIMIT_ENABLED", "")
	t.Setenv("ONEOPS_RATE_LIMIT_RPS", "")
	t.Setenv("ONEOPS_RATE_LIMIT_BURST", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !c.RateLimitEnabled {
		t.Error("RateLimitEnabled = false, want true (secure default)")
	}
	if c.RateLimitRPS <= 0 || c.RateLimitBurst <= 0 {
		t.Errorf("RateLimitRPS=%d RateLimitBurst=%d, want positive defaults",
			c.RateLimitRPS, c.RateLimitBurst)
	}
}

// Rate limiting enabled with a zero budget would serve nothing while claiming
// to be configured — a config error, not a runtime one.
func TestLoadRejectsRateLimitEnabledWithZeroRPS(t *testing.T) {
	t.Setenv("ONEOPS_RATE_LIMIT_ENABLED", "true")
	t.Setenv("ONEOPS_RATE_LIMIT_RPS", "0")
	t.Setenv("ONEOPS_RATE_LIMIT_BURST", "40")

	if _, err := Load(); err == nil {
		t.Fatal("expected error for rate limiting enabled with rps=0, got nil")
	}
}

func TestLoadRejectsRateLimitEnabledWithZeroBurst(t *testing.T) {
	t.Setenv("ONEOPS_RATE_LIMIT_ENABLED", "true")
	t.Setenv("ONEOPS_RATE_LIMIT_RPS", "20")
	t.Setenv("ONEOPS_RATE_LIMIT_BURST", "0")

	if _, err := Load(); err == nil {
		t.Fatal("expected error for rate limiting enabled with burst=0, got nil")
	}
}

// Disabling rate limiting is the documented rollback path and must not be
// blocked by the same guard that protects the enabled case.
func TestLoadAllowsRateLimitDisabledWithZeroValues(t *testing.T) {
	t.Setenv("ONEOPS_RATE_LIMIT_ENABLED", "false")
	t.Setenv("ONEOPS_RATE_LIMIT_RPS", "0")
	t.Setenv("ONEOPS_RATE_LIMIT_BURST", "0")

	if _, err := Load(); err != nil {
		t.Fatalf("unexpected error with rate limiting disabled: %v", err)
	}
}

// An empty ONEOPS_ADDITIONAL_IDPS must not change a single-IdP deployment's
// behavior at all — this is the backward-compat contract for enterprise SSO.
func TestLoadDefaultsToNoAdditionalIDPs(t *testing.T) {
	t.Setenv("ONEOPS_ADDITIONAL_IDPS", "")

	c, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.AdditionalIDPs) != 0 {
		t.Errorf("AdditionalIDPs = %+v, want empty", c.AdditionalIDPs)
	}
}

func TestLoadParsesAdditionalIDPs(t *testing.T) {
	t.Setenv("ONEOPS_ADDITIONAL_IDPS", `[
		{"issuer":"https://idp-a.example","audience":"aud-a","jwks_url":"https://idp-a.example/jwks"},
		{"issuer":"https://idp-b.example","audience":"aud-b","hmac_key":"idp-b-secret"}
	]`)

	c, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(c.AdditionalIDPs) != 2 {
		t.Fatalf("AdditionalIDPs = %+v, want 2 entries", c.AdditionalIDPs)
	}
	if c.AdditionalIDPs[0].Issuer != "https://idp-a.example" || c.AdditionalIDPs[0].JWKSURL == "" {
		t.Errorf("idp A parsed wrong: %+v", c.AdditionalIDPs[0])
	}
	if c.AdditionalIDPs[1].HMACKey != "idp-b-secret" {
		t.Errorf("idp B parsed wrong: %+v", c.AdditionalIDPs[1])
	}
}

func TestLoadRejectsMalformedAdditionalIDPs(t *testing.T) {
	t.Setenv("ONEOPS_ADDITIONAL_IDPS", `not-json`)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for malformed ONEOPS_ADDITIONAL_IDPS")
	}
}

func TestLoadRejectsAdditionalIdPMissingAudience(t *testing.T) {
	t.Setenv("ONEOPS_ADDITIONAL_IDPS", `[{"issuer":"https://idp-a.example","jwks_url":"https://idp-a.example/jwks"}]`)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for an additional IdP missing an audience")
	}
}

func TestLoadRejectsAdditionalIdPMissingKeys(t *testing.T) {
	t.Setenv("ONEOPS_ADDITIONAL_IDPS", `[{"issuer":"https://idp-a.example","audience":"aud-a"}]`)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for an additional IdP with neither jwks_url nor hmac_key")
	}
}

func TestLoadRejectsDuplicateAdditionalIdPIssuers(t *testing.T) {
	t.Setenv("ONEOPS_ADDITIONAL_IDPS", `[
		{"issuer":"https://idp-a.example","audience":"aud-a","hmac_key":"s1"},
		{"issuer":"https://idp-a.example","audience":"aud-a2","hmac_key":"s2"}
	]`)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for duplicate issuers across additional IdPs")
	}
}

// An additional IdP cannot silently shadow the default: colliding with
// ONEOPS_JWT_ISSUER is a duplicate issuer, same as colliding with another
// additional IdP.
func TestLoadRejectsAdditionalIdPCollidingWithDefaultIssuer(t *testing.T) {
	t.Setenv("ONEOPS_JWT_ISSUER", "https://oneops.local")
	t.Setenv("ONEOPS_ADDITIONAL_IDPS", `[{"issuer":"https://oneops.local","audience":"other","hmac_key":"s1"}]`)
	if _, err := Load(); err == nil {
		t.Fatal("expected error for an additional IdP reusing the default issuer")
	}
}
