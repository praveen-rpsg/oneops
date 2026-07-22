// Package config loads OneOps control-plane configuration from the environment
// (12-factor). All values have safe production-shaped defaults for local dev.
package config

import (
	"fmt"
	"os"
	"strconv"
)

// Config holds the resolved runtime configuration.
type Config struct {
	HTTPAddr      string
	Env           string
	ShutdownGrace int

	// Database.
	DatabaseURL string
	DBMaxConns  int
	AutoMigrate bool

	// Pagination.
	MaxPageSize     int
	DefaultPageSize int

	// Auth.
	AuthEnabled bool
	JWTIssuer   string
	JWTAudience string
	JWTHMACKey  string // HS256 dev/test secret; empty => RS256/JWKS
	JWKSURL     string // RS256 public keys (OIDC)

	// Observability.
	ServiceName  string
	OTLPEndpoint string // empty => tracing exporter disabled
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	c := &Config{
		HTTPAddr:        getEnv("ONEOPS_HTTP_ADDR", ":8080"),
		Env:             getEnv("ONEOPS_ENV", "dev"),
		ShutdownGrace:   getEnvInt("ONEOPS_SHUTDOWN_GRACE_SECONDS", 10),
		DatabaseURL:     getEnv("ONEOPS_DB_URL", "postgres://oneops:dev@localhost:5432/oneops?sslmode=disable"),
		DBMaxConns:      getEnvInt("ONEOPS_DB_MAX_CONNS", 10),
		AutoMigrate:     getEnvBool("ONEOPS_AUTO_MIGRATE", true),
		MaxPageSize:     getEnvInt("ONEOPS_MAX_PAGE_SIZE", 200),
		DefaultPageSize: getEnvInt("ONEOPS_DEFAULT_PAGE_SIZE", 50),
		AuthEnabled:     getEnvBool("ONEOPS_AUTH_ENABLED", true),
		JWTIssuer:       getEnv("ONEOPS_JWT_ISSUER", "https://oneops.local"),
		JWTAudience:     getEnv("ONEOPS_JWT_AUDIENCE", "oneops"),
		JWTHMACKey:      getEnv("ONEOPS_JWT_HMAC_KEY", "dev-insecure-secret-change-me"),
		JWKSURL:         getEnv("ONEOPS_JWKS_URL", ""),
		ServiceName:     getEnv("ONEOPS_SERVICE_NAME", "oneops-controlplane"),
		OTLPEndpoint:    getEnv("ONEOPS_OTLP_ENDPOINT", ""),
	}
	if c.ShutdownGrace < 0 {
		return nil, fmt.Errorf("ONEOPS_SHUTDOWN_GRACE_SECONDS must be >= 0, got %d", c.ShutdownGrace)
	}
	if c.HTTPAddr == "" {
		return nil, fmt.Errorf("ONEOPS_HTTP_ADDR must not be empty")
	}
	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("ONEOPS_DB_URL must not be empty")
	}
	if c.MaxPageSize <= 0 || c.DefaultPageSize <= 0 || c.DefaultPageSize > c.MaxPageSize {
		return nil, fmt.Errorf("invalid page sizes: default=%d max=%d", c.DefaultPageSize, c.MaxPageSize)
	}
	if c.AuthEnabled && c.JWTHMACKey == "" && c.JWKSURL == "" {
		return nil, fmt.Errorf("auth enabled but neither ONEOPS_JWT_HMAC_KEY nor ONEOPS_JWKS_URL is set")
	}
	return c, nil
}

func getEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getEnvBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
