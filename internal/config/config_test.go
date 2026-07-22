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
