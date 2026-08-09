// Package config loads OneOps control-plane configuration from the environment
// (12-factor). All values have safe production-shaped defaults for local dev.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Insecure development defaults that must never reach a production environment.
const (
	devJWTHMACKey  = "dev-insecure-secret-change-me"
	devDatabaseURL = "postgres://oneops:dev@localhost:5432/oneops?sslmode=disable"
)

// Config holds the resolved runtime configuration.
type Config struct {
	HTTPAddr string
	// MetricsAddr binds the observability listener. /metrics is served there
	// and not on the public listener, so scrape exposure is decided by
	// deployment topology rather than by an authentication check Prometheus
	// would have to be taught to satisfy. Empty keeps /metrics on the public
	// listener, which is the pre-existing behaviour and is intended only for
	// local development.
	MetricsAddr   string
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

	// JWKSAllowPrivateTargets disables the SSRF egress guard for the default
	// IdP's JWKS fetch, permitting loopback/private-network addresses —
	// mirroring WebhookAllowPrivateTargets for the same guard on outbound
	// delivery (ADR-SECURITY-001/003). The secure default is false. Set true
	// only when ONEOPS_JWKS_URL deliberately points at a private/on-prem IdP
	// (a self-hosted IdP inside the customer's VPN, or a local bundled IdP
	// such as the demo Keycloak in docker-compose.auth.yml on loopback).
	JWKSAllowPrivateTargets bool

	// AdditionalIDPs are trusted identity providers beyond the default
	// (JWTIssuer/JWTAudience/...) — enterprise SSO, where different tenants
	// bring their own IdP. Configured via ONEOPS_ADDITIONAL_IDPS; empty is the
	// common case and changes nothing about the default IdP's behavior.
	AdditionalIDPs []IDPSpec

	// OIDC client identity for the browser console. The server never uses it —
	// it is published at /auth/config so a per-deployment console can configure
	// itself against the customer's own identity provider without a rebuild.
	OIDCClientID string

	// Observability.
	ServiceName  string
	OTLPEndpoint string // empty => tracing exporter disabled

	// Audit-integrity verification scheduler (operational).
	VerifyIntervalSeconds int // 0 => scheduler disabled
	// SchemaSentinelIntervalSeconds is how often the ownership-model schema
	// invariants are re-verified for the life of the process (ADR-SECURITY-002).
	SchemaSentinelIntervalSeconds int
	VerifyTimeoutSeconds          int // per-chain verification timeout
	VerifyRetryAttempts           int // retries on transport error (never on a break)

	// Runtime profiling (operational). Disabled by default; mounts net/http/pprof.
	PProfEnabled bool

	// Webhook delivery retention in hours (terminal deliveries older than this are
	// pruned; pending/failed are never deleted). 0 disables deletion.
	WebhookRetentionHours int

	// WebhookAllowPrivateTargets disables the SSRF egress guard, permitting
	// webhook and policy-action delivery to loopback, link-local, and private
	// addresses. The secure default is false: the platform refuses to POST to
	// non-public addresses so a tenant cannot make it a confused deputy against
	// internal services or the cloud metadata endpoint (ADR-SECURITY-001). Set
	// true only for a deployment whose delivery targets are legitimately on a
	// private network and whose tenants are trusted to address them.
	WebhookAllowPrivateTargets bool

	// Per-tenant rate limiting (ADR-SECURITY-004): an in-memory, per-instance
	// token bucket keyed by tenant. With N replicas the effective global limit
	// for a tenant is approximately N times RateLimitRPS/RateLimitBurst — there
	// is no cross-instance state. RateLimitEnabled=false is a pass-through, for
	// a clean rollback.
	RateLimitEnabled bool
	RateLimitRPS     int
	RateLimitBurst   int
}

// IDPSpec is one entry of ONEOPS_ADDITIONAL_IDPS: a trusted identity provider
// beyond the default. Each needs an issuer, an audience, and at least one key
// source — a JWKSURL for RS256 (a real IdP), or an HMACKey for HS256
// (dev/test only; production rejects the shared insecure default the same
// way it does for the default IdP, see validateProduction).
type IDPSpec struct {
	Issuer   string `json:"issuer"`
	Audience string `json:"audience"`
	JWKSURL  string `json:"jwks_url,omitempty"`
	HMACKey  string `json:"hmac_key,omitempty"`
}

// IsProduction reports whether the service runs in a production environment,
// where insecure development defaults are rejected at startup.
func (c *Config) IsProduction() bool {
	switch strings.ToLower(strings.TrimSpace(c.Env)) {
	case "prod", "production":
		return true
	default:
		return false
	}
}

// validateProduction fails fast when a production environment is left on insecure
// development defaults. It is a no-op outside production.
func (c *Config) validateProduction() error {
	if !c.IsProduction() {
		return nil
	}
	var problems []string
	if c.AuthEnabled && c.JWTHMACKey == devJWTHMACKey {
		problems = append(problems, "ONEOPS_JWT_HMAC_KEY is the insecure development default")
	}
	for _, idp := range c.AdditionalIDPs {
		if idp.HMACKey == devJWTHMACKey {
			problems = append(problems, fmt.Sprintf(
				"ONEOPS_ADDITIONAL_IDPS entry %q uses the insecure development HMAC key", idp.Issuer))
		}
	}
	if !c.AuthEnabled {
		problems = append(problems, "ONEOPS_AUTH_ENABLED must not be false in production")
	}
	if c.DatabaseURL == devDatabaseURL {
		problems = append(problems, "ONEOPS_DB_URL is the local development default")
	}
	if strings.Contains(c.DatabaseURL, "sslmode=disable") {
		problems = append(problems, "ONEOPS_DB_URL disables TLS (sslmode=disable)")
	}
	if c.MetricsAddr == "" {
		// /metrics discloses audit-integrity state, per-route request volumes
		// and dependency health to anyone who can reach the public listener.
		// None of it is tenant data, but it tells an attacker when audit
		// verification is failing.
		problems = append(problems, "ONEOPS_METRICS_ADDR is empty, which serves /metrics on the public listener")
	}
	if len(problems) > 0 {
		return fmt.Errorf("insecure production configuration: %s", strings.Join(problems, "; "))
	}
	return nil
}

// validateAdditionalIDPs rejects a multi-IdP configuration that could not be
// built into a working Verifier: a missing issuer/audience, an IdP with no
// key source, or an issuer that collides with another additional IdP or with
// the default IdP (ONEOPS_JWT_ISSUER). This mirrors the checks
// auth.NewMultiVerifier makes at construction, so a bad config fails fast at
// Load rather than at first request.
func (c *Config) validateAdditionalIDPs() error {
	if len(c.AdditionalIDPs) == 0 {
		return nil
	}
	seen := map[string]bool{c.JWTIssuer: true}
	for _, idp := range c.AdditionalIDPs {
		if idp.Issuer == "" || idp.Audience == "" {
			return fmt.Errorf("ONEOPS_ADDITIONAL_IDPS entry missing issuer or audience")
		}
		if idp.JWKSURL == "" && idp.HMACKey == "" {
			return fmt.Errorf("ONEOPS_ADDITIONAL_IDPS entry %q needs a jwks_url or hmac_key", idp.Issuer)
		}
		if seen[idp.Issuer] {
			return fmt.Errorf("ONEOPS_ADDITIONAL_IDPS has duplicate issuer %q", idp.Issuer)
		}
		seen[idp.Issuer] = true
	}
	return nil
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	c := &Config{
		HTTPAddr:                getEnv("ONEOPS_HTTP_ADDR", ":8080"),
		MetricsAddr:             getEnv("ONEOPS_METRICS_ADDR", ":9090"),
		Env:                     getEnv("ONEOPS_ENV", "dev"),
		ShutdownGrace:           getEnvInt("ONEOPS_SHUTDOWN_GRACE_SECONDS", 10),
		DatabaseURL:             getEnv("ONEOPS_DB_URL", "postgres://oneops:dev@localhost:5432/oneops?sslmode=disable"),
		DBMaxConns:              getEnvInt("ONEOPS_DB_MAX_CONNS", 10),
		AutoMigrate:             getEnvBool("ONEOPS_AUTO_MIGRATE", true),
		MaxPageSize:             getEnvInt("ONEOPS_MAX_PAGE_SIZE", 200),
		DefaultPageSize:         getEnvInt("ONEOPS_DEFAULT_PAGE_SIZE", 50),
		AuthEnabled:             getEnvBool("ONEOPS_AUTH_ENABLED", true),
		JWTIssuer:               getEnv("ONEOPS_JWT_ISSUER", "https://oneops.local"),
		JWTAudience:             getEnv("ONEOPS_JWT_AUDIENCE", "oneops"),
		JWTHMACKey:              getEnv("ONEOPS_JWT_HMAC_KEY", "dev-insecure-secret-change-me"),
		JWKSURL:                 getEnv("ONEOPS_JWKS_URL", ""),
		JWKSAllowPrivateTargets: getEnvBool("ONEOPS_JWKS_ALLOW_PRIVATE_TARGETS", false),
		OIDCClientID:            getEnv("ONEOPS_OIDC_CLIENT_ID", ""),
		ServiceName:             getEnv("ONEOPS_SERVICE_NAME", "oneops-controlplane"),
		OTLPEndpoint:            getEnv("ONEOPS_OTLP_ENDPOINT", ""),

		VerifyIntervalSeconds:         getEnvInt("ONEOPS_AUDIT_VERIFY_INTERVAL_SECONDS", 300),
		SchemaSentinelIntervalSeconds: getEnvInt("ONEOPS_SCHEMA_SENTINEL_INTERVAL_SECONDS", 30),
		VerifyTimeoutSeconds:          getEnvInt("ONEOPS_AUDIT_VERIFY_TIMEOUT_SECONDS", 30),
		VerifyRetryAttempts:           getEnvInt("ONEOPS_AUDIT_VERIFY_RETRY_ATTEMPTS", 2),

		PProfEnabled: getEnvBool("ONEOPS_PPROF_ENABLED", false),

		WebhookRetentionHours:      getEnvInt("ONEOPS_WEBHOOK_RETENTION_HOURS", 720),
		WebhookAllowPrivateTargets: getEnvBool("ONEOPS_WEBHOOK_ALLOW_PRIVATE_TARGETS", false),

		RateLimitEnabled: getEnvBool("ONEOPS_RATE_LIMIT_ENABLED", true),
		RateLimitRPS:     getEnvInt("ONEOPS_RATE_LIMIT_RPS", 20),
		RateLimitBurst:   getEnvInt("ONEOPS_RATE_LIMIT_BURST", 40),
	}
	if raw := getEnv("ONEOPS_ADDITIONAL_IDPS", ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &c.AdditionalIDPs); err != nil {
			return nil, fmt.Errorf("ONEOPS_ADDITIONAL_IDPS is not valid JSON: %w", err)
		}
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
	if err := c.validateAdditionalIDPs(); err != nil {
		return nil, err
	}
	if c.VerifyIntervalSeconds < 0 || c.VerifyTimeoutSeconds < 0 || c.VerifyRetryAttempts < 0 {
		return nil, fmt.Errorf("audit verify settings must be >= 0")
	}
	if c.RateLimitEnabled && (c.RateLimitRPS <= 0 || c.RateLimitBurst <= 0) {
		return nil, fmt.Errorf(
			"rate limiting enabled but ONEOPS_RATE_LIMIT_RPS/ONEOPS_RATE_LIMIT_BURST must be > 0 (rps=%d burst=%d)",
			c.RateLimitRPS, c.RateLimitBurst)
	}
	if err := c.validateProduction(); err != nil {
		return nil, err
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
