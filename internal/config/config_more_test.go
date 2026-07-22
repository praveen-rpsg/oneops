package config

import "testing"

func TestLoadPageSizeInvalid(t *testing.T) {
	t.Setenv("ONEOPS_DEFAULT_PAGE_SIZE", "500")
	t.Setenv("ONEOPS_MAX_PAGE_SIZE", "100")
	if _, err := Load(); err == nil {
		t.Fatal("expected page-size validation error")
	}
}

func TestLoadZeroPageSize(t *testing.T) {
	t.Setenv("ONEOPS_DEFAULT_PAGE_SIZE", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected zero page-size validation error")
	}
}

func TestGetEnvBoolAndInt(t *testing.T) {
	t.Setenv("ONEOPS_AUTO_MIGRATE", "false")
	t.Setenv("ONEOPS_MAX_PAGE_SIZE", "not-a-number") // falls back to default
	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.AutoMigrate {
		t.Error("AutoMigrate should be false")
	}
	if c.MaxPageSize != 200 {
		t.Errorf("MaxPageSize fallback = %d, want 200", c.MaxPageSize)
	}
}

func TestLoadOverridesDBAndOTel(t *testing.T) {
	t.Setenv("ONEOPS_DB_URL", "postgres://u:p@db:5432/x")
	t.Setenv("ONEOPS_OTLP_ENDPOINT", "collector:4317")
	t.Setenv("ONEOPS_SERVICE_NAME", "svc")
	c, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.DatabaseURL != "postgres://u:p@db:5432/x" {
		t.Errorf("db url = %q", c.DatabaseURL)
	}
	if c.OTLPEndpoint != "collector:4317" || c.ServiceName != "svc" {
		t.Errorf("otel config = %q / %q", c.OTLPEndpoint, c.ServiceName)
	}
}
