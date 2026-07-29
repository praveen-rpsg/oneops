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
}

// Verifier validates bearer tokens. It supports HS256 (shared secret, for
// local/dev and tests) and RS256 (via a JWKS endpoint, for OIDC providers).
type Verifier struct {
	issuer   string
	audience string
	hmacKey  []byte
	jwks     *jwksCache
}

// NewVerifier builds a Verifier. hmacKey enables HS256; jwksURL enables RS256.
func NewVerifier(issuer, audience, hmacKey, jwksURL string) *Verifier {
	return NewVerifierWithClient(issuer, audience, hmacKey, jwksURL, nil)
}

// NewVerifierWithClient is NewVerifier with an explicit HTTP client for the JWKS
// fetch. A nil client means the SSRF-guarded default; tests supply their own to
// reach a local server (ADR-SECURITY-003).
func NewVerifierWithClient(issuer, audience, hmacKey, jwksURL string, doer *http.Client) *Verifier {
	v := &Verifier{issuer: issuer, audience: audience}
	if hmacKey != "" {
		v.hmacKey = []byte(hmacKey)
	}
	if jwksURL != "" {
		v.jwks = newJWKSCache(jwksURL, doer)
	}
	return v
}

// Verify parses and validates a raw token and returns its claims.
func (v *Verifier) Verify(raw string) (*Claims, error) {
	parsed, err := jwt.Parse(raw, v.keyfunc,
		jwt.WithValidMethods([]string{"HS256", "RS256"}),
		jwt.WithIssuer(v.issuer),
		jwt.WithAudience(v.audience),
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
	return &Claims{Subject: sub, Roles: parseRoles(mc["roles"]), Tenant: tenant}, nil
}

func (v *Verifier) keyfunc(t *jwt.Token) (any, error) {
	switch t.Method.Alg() {
	case "HS256":
		if len(v.hmacKey) == 0 {
			return nil, fmt.Errorf("HS256 tokens not accepted")
		}
		return v.hmacKey, nil
	case "RS256":
		if v.jwks == nil {
			return nil, fmt.Errorf("RS256 tokens not accepted")
		}
		kid, _ := t.Header["kid"].(string)
		return v.jwks.key(kid)
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

func newJWKSCache(url string, doer *http.Client) *jwksCache {
	if doer == nil {
		doer = safehttp.Client(10*time.Second, false)
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
