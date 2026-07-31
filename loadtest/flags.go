//go:build loadtest

package main

import (
	"flag"
	"time"
)

type config struct {
	baseURL        string
	workers        int
	duration       time.Duration
	requests       int // 0 => run for duration instead of a fixed count
	seed           int
	requestTimeout time.Duration

	jwtIssuer   string
	jwtAudience string
	jwtSecret   string
	jwtRole     string
}

// parseFlags reads harness configuration. Defaults for the JWT fields match
// internal/config.Load's dev defaults (ONEOPS_JWT_ISSUER/AUDIENCE/HMAC_KEY);
// override them with the flags below when the target instance overrides those
// environment variables.
func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.baseURL, "base-url", "http://localhost:8080", "base URL of the running control-plane instance")
	flag.IntVar(&cfg.workers, "workers", 20, "number of concurrent workers")
	flag.DurationVar(&cfg.duration, "duration", 30*time.Second, "how long to run (ignored if -requests > 0)")
	flag.IntVar(&cfg.requests, "requests", 0, "total request count; overrides -duration when > 0")
	flag.IntVar(&cfg.seed, "seed", 20, "number of artifacts to create before the run, read back during it")
	flag.DurationVar(&cfg.requestTimeout, "timeout", 10*time.Second, "per-request timeout")
	flag.StringVar(&cfg.jwtIssuer, "jwt-issuer", "https://oneops.local", "JWT issuer claim (must match ONEOPS_JWT_ISSUER on the target)")
	flag.StringVar(&cfg.jwtAudience, "jwt-audience", "oneops", "JWT audience claim (must match ONEOPS_JWT_AUDIENCE on the target)")
	flag.StringVar(&cfg.jwtSecret, "jwt-secret", "dev-insecure-secret-change-me", "HS256 signing secret (must match ONEOPS_JWT_HMAC_KEY on the target)")
	flag.StringVar(&cfg.jwtRole, "jwt-role", "oneops-admin", "role claim minted into the token (oneops-admin grants read+write, matching the mix)")
	flag.Parse()
	return cfg
}
