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
