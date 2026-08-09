// Package auth verifies OIDC/JWT bearer tokens and evaluates RBAC permissions.
package auth

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/rpsg/oneops/internal/safehttp"
)

// Claims is the parsed, verified identity extracted from a token.
type Claims struct {
	Subject string
	Roles   []string
	Tenant  string
	// Issuer is the VERIFIED `iss` of the token — the value jwt.Parse validated
	// against the matched IdP, not merely the one peeked to select the key. The
	// authentication boundary uses it to enforce that the token's issuer is
	// authorised for the tenant it claims (ADR-IDENTITY-003), which is what
	// stops one configured IdP from authenticating another tenant.
	Issuer string
}

// IDPConfig describes one trusted identity provider: enterprise SSO means
// different tenants bring their own IdP, each with its own issuer, expected
// audience, and key source (HS256 shared secret for dev/test, or RS256 via a
// JWKS endpoint for a real OIDC provider).
type IDPConfig struct {
	Issuer   string
	Audience string
	HMACKey  string
	JWKSURL  string
	// AllowPrivateJWKS opts this IdP's JWKS fetch out of the SSRF guard's
	// public-address requirement (ADR-SECURITY-001/003), mirroring
	// config.WebhookAllowPrivateTargets for the same guard on outbound
	// delivery. False by default — the secure posture (public addresses
	// only) is unchanged. Set true only for an IdP an operator has
	// deliberately placed on a private network: a self-hosted/on-prem IdP
	// reachable solely inside the customer's VPN, or a bundled local IdP
	// (e.g. the demo Keycloak in docker-compose.auth.yml, reached over
	// loopback). Ignored when Client is set explicitly.
	AllowPrivateJWKS bool
	// Client is the HTTP client used for this IdP's JWKS fetch. nil means the
	// SSRF-guarded default (ADR-SECURITY-003), gated by AllowPrivateJWKS;
	// tests inject their own to reach a local server.
	Client *http.Client
}

// idp holds one IDPConfig resolved into verification material.
type idp struct {
	audience string
	hmacKey  []byte
	jwks     *jwksCache
}

// Verifier validates bearer tokens against a set of trusted identity
// providers, selected by the token's `iss` claim. It supports HS256 (shared
// secret, for local/dev and tests) and RS256 (via a JWKS endpoint, for OIDC
// providers).
type Verifier struct {
	// idps is keyed by issuer. A token whose issuer has no entry here is
	// rejected outright — there is no default IdP to fall through to, so
	// adding an IdP can only narrow what is accepted, never widen it.
	idps map[string]*idp
}

// NewVerifier builds a single-IdP Verifier. hmacKey enables HS256; jwksURL
// enables RS256. This is the backward-compatible entry point for a single
// trusted issuer.
func NewVerifier(issuer, audience, hmacKey, jwksURL string) *Verifier {
	return NewVerifierWithClient(issuer, audience, hmacKey, jwksURL, nil)
}

// NewVerifierWithClient is NewVerifier with an explicit HTTP client for the JWKS
// fetch. A nil client means the SSRF-guarded default; tests supply their own to
// reach a local server (ADR-SECURITY-003).
func NewVerifierWithClient(issuer, audience, hmacKey, jwksURL string, doer *http.Client) *Verifier {
	v, err := NewMultiVerifier([]IDPConfig{{
		Issuer: issuer, Audience: audience, HMACKey: hmacKey, JWKSURL: jwksURL, Client: doer,
	}})
	if err != nil {
		// A single IDPConfig can only fail construction on a duplicate
		// issuer, which is impossible with exactly one entry.
		panic(err)
	}
	return v
}

// NewMultiVerifier builds a Verifier trusting every IdP in configs, keyed by
// issuer. Duplicate issuers and configs missing an issuer are rejected so a
// misconfiguration cannot silently shadow another IdP.
func NewMultiVerifier(configs []IDPConfig) (*Verifier, error) {
	idps := make(map[string]*idp, len(configs))
	for _, c := range configs {
		if c.Issuer == "" {
			return nil, fmt.Errorf("idp config missing issuer")
		}
		if _, exists := idps[c.Issuer]; exists {
			return nil, fmt.Errorf("duplicate issuer %q", c.Issuer)
		}
		e := &idp{audience: c.Audience}
		if c.HMACKey != "" {
			e.hmacKey = []byte(c.HMACKey)
		}
		if c.JWKSURL != "" {
			e.jwks = newJWKSCache(c.JWKSURL, c.Client, c.AllowPrivateJWKS)
		}
		idps[c.Issuer] = e
	}
	return &Verifier{idps: idps}, nil
}

// Verify parses and validates a raw token and returns its claims. The token's
// `iss` claim selects which IdP verifies it; issuer, audience, signature and
// expiration are all checked against that IdP alone, never a default.
func (v *Verifier) Verify(raw string) (*Claims, error) {
	iss, err := peekIssuer(raw)
	if err != nil {
		return nil, fmt.Errorf("token verification failed: %w", err)
	}
	e, ok := v.idps[iss]
	if !ok {
		return nil, fmt.Errorf("token verification failed: unrecognized issuer %q", iss)
	}
	parsed, err := jwt.Parse(raw, e.keyfunc,
		jwt.WithValidMethods([]string{"HS256", "RS256"}),
		jwt.WithIssuer(iss),
		jwt.WithAudience(e.audience),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("token verification failed: %w", err)
	}
	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, fmt.Errorf("unexpected claims type")
	}
	sub, _ := mc["sub"].(string)
	if sub == "" {
		return nil, fmt.Errorf("missing subject claim")
	}
	tenant, _ := mc["tenant"].(string)
	if tenant == "" {
		tenant, _ = mc["tid"].(string)
	}
	// The issuer carried out is the one jwt.Parse validated (WithIssuer(iss)
	// above required the token's own `iss` to equal it), not the unverified
	// peeked value — so a downstream authorization decision rests on a checked
	// claim.
	verifiedIss, _ := mc.GetIssuer()
	return &Claims{
		Subject: sub, Roles: parseRoles(mc["roles"]), Tenant: tenant, Issuer: verifiedIss,
	}, nil
}

// peekIssuer reads the `iss` claim without verifying the signature, so the
// verifier can pick the right IdP's keys before checking anything else. The
// signature, audience and expiration of whatever issuer this names are still
// fully verified afterwards by Verify — this only ever narrows key selection,
// it establishes no trust.
func peekIssuer(raw string) (string, error) {
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(raw, claims); err != nil {
		return "", fmt.Errorf("parse token: %w", err)
	}
	iss, _ := claims.GetIssuer()
	if iss == "" {
		return "", fmt.Errorf("missing issuer claim")
	}
	return iss, nil
}

func (e *idp) keyfunc(t *jwt.Token) (any, error) {
	switch t.Method.Alg() {
	case "HS256":
		if len(e.hmacKey) == 0 {
			return nil, fmt.Errorf("HS256 tokens not accepted")
		}
		return e.hmacKey, nil
	case "RS256":
		if e.jwks == nil {
			return nil, fmt.Errorf("RS256 tokens not accepted")
		}
		kid, _ := t.Header["kid"].(string)
		return e.jwks.key(kid)
	default:
		return nil, fmt.Errorf("unexpected signing method %q", t.Method.Alg())
	}
}

func parseRoles(v any) []string {
	switch val := v.(type) {
	case []string:
		return val
	case []any:
		out := make([]string, 0, len(val))
		for _, r := range val {
			if s, ok := r.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if val == "" {
			return nil
		}
		return []string{val}
	default:
		return nil
	}
}

// jwksCache fetches and caches RSA public keys from a JWKS endpoint.
type jwksCache struct {
	url string
	// doer fetches the JWKS document. It is the SSRF-guarded client
	// (ADR-SECURITY-001/003), never http.DefaultClient: this fetch dials a
	// configured URL from inside the platform, so an unguarded client here is the
	// same confused-deputy shape the outbound delivery clients already close, and
	// it also had no timeout — a hung JWKS endpoint stalled every RS256
	// verification behind it.
	doer *http.Client
	mu   sync.RWMutex
	// keys maps kid -> public key.
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
	ttl       time.Duration
}

func newJWKSCache(url string, doer *http.Client, allowPrivate bool) *jwksCache {
	if doer == nil {
		doer = safehttp.Client(10*time.Second, allowPrivate)
	}
	return &jwksCache{url: url, doer: doer, keys: map[string]*rsa.PublicKey{}, ttl: 15 * time.Minute}
}

func (c *jwksCache) key(kid string) (*rsa.PublicKey, error) {
	c.mu.RLock()
	k, ok := c.keys[kid]
	fresh := time.Since(c.fetchedAt) < c.ttl
	c.mu.RUnlock()
	if ok && fresh {
		return k, nil
	}
	if err := c.refresh(); err != nil {
		return nil, err
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if k, ok := c.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("unknown key id %q", kid)
}

type jwk struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (c *jwksCache) refresh() error {
	req, err := http.NewRequest(http.MethodGet, c.url, nil) //nolint:noctx // bounded by the client timeout
	if err != nil {
		return fmt.Errorf("build jwks request: %w", err)
	}
	resp, err := c.doer.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks endpoint returned %d", resp.StatusCode)
	}
	var doc struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return fmt.Errorf("decode jwks: %w", err)
	}
	next := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, k := range doc.Keys {
		if k.Kty != "RSA" {
			continue
		}
		pub, err := k.toRSA()
		if err != nil {
			return err
		}
		next[k.Kid] = pub
	}
	c.mu.Lock()
	c.keys = next
	c.fetchedAt = time.Now()
	c.mu.Unlock()
	return nil
}

func (k jwk) toRSA() (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, fmt.Errorf("decode modulus: %w", err)
	}
	eb, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, fmt.Errorf("decode exponent: %w", err)
	}
	e := 0
	for _, b := range eb {
		e = e<<8 | int(b)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}, nil
}
