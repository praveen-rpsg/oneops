package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
)

// A second, additional identity provider — "idp-B" — as an enterprise-SSO
// deployment would configure for a tenant that brings its own IdP. It is a
// genuinely trusted issuer: the verifier accepts its signatures. The bypass
// under test is that a genuine idp-B token could claim ANY tenant's external id.
const (
	idpBIss    = "https://idp-b.example"
	idpBAud    = "aud-b"
	idpBSecret = "idp-b-secret"
)

// newMultiIDPServer builds a Server whose verifier trusts the default IdP
// (tIss/tAud/tSecret, the ONEOPS_JWT_ISSUER of this deployment) AND idp-B. This
// is the multi-IdP configuration OPS-S047a introduced and is the precondition
// for the cross-tenant bypass OPS-S047b closes.
func newMultiIDPServer(t *testing.T) *Server {
	t.Helper()
	repo := newFakeRepo()
	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	verifier, err := auth.NewMultiVerifier([]auth.IDPConfig{
		{Issuer: tIss, Audience: tAud, HMACKey: tSecret},
		{Issuer: idpBIss, Audience: idpBAud, HMACKey: idpBSecret},
	})
	if err != nil {
		t.Fatalf("NewMultiVerifier: %v", err)
	}
	return NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		repo, newFakeIdem(), verifier,
		observability.NewMetrics(), func(context.Context) error { return nil })
}

// mintIssuerToken signs an HS256 token for an arbitrary issuer/audience/secret,
// asserting a tenant external id. This is how an actor controlling idp-B mints a
// validly-signed token that claims another tenant.
func mintIssuerToken(t *testing.T, iss, aud, secret, tenantExternal string) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub": "u", "iss": iss, "aud": aud,
		"exp":   time.Now().Add(time.Hour).Unix(),
		"roles": []string{"oneops-admin"},
	}
	if tenantExternal != "" {
		claims["tenant"] = tenantExternal
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func getArtifacts(t *testing.T, srv *Server, token string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/artifacts", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec.Code
}

// THE EXPLOIT (ADR-IDENTITY-003, Trust-Register). Tenant X allows only the
// default IdP (empty allowed_issuers). An actor controlling idp-B — a genuine,
// configured issuer — mints a validly-signed token asserting tenant X's external
// id. Before the issuer-to-tenant binding, resolveTenant looked X up purely by
// external id and admitted the request into X's row-level-security boundary: a
// cross-tenant authorization bypass. After the fix, the request is rejected with
// 403 and never enters X's boundary.
//
// This test BITES: revert the AllowsIssuer enforcement in resolveTenant and the
// request is admitted (200), failing this assertion.
func TestIssuerBinding_ForeignIdPCannotAuthenticateAnotherTenant(t *testing.T) {
	srv := newMultiIDPServer(t)
	// Tenant X: allowed_issuers empty => only the default IdP may authenticate it.
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))

	// idp-B token claiming tenant X's external id. The signature is genuine; the
	// verifier accepts it. The only thing wrong is that idp-B is not authorised
	// to authenticate tenant X.
	token := mintIssuerToken(t, idpBIss, idpBAud, idpBSecret, "ext-acme")

	if code := getArtifacts(t, srv, token); code != http.StatusForbidden {
		t.Fatalf("idp-B token claiming tenant X = %d, want 403 "+
			"(a foreign IdP was admitted into another tenant's boundary — cross-tenant bypass)", code)
	}
}

// BACKWARD COMPATIBILITY. A tenant with empty allowed_issuers keeps working with
// the default IdP, exactly as before multi-IdP support existed. This is what
// makes the safe-by-default rule non-breaking for every pre-existing tenant.
func TestIssuerBinding_DefaultIdPStillAuthenticatesEmptyBindingTenant(t *testing.T) {
	srv := newMultiIDPServer(t)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))

	token := mintIssuerToken(t, tIss, tAud, tSecret, "ext-acme")

	if code := getArtifacts(t, srv, token); code != http.StatusOK {
		t.Fatalf("default IdP token for an empty-binding tenant = %d, want 200 "+
			"(backward compatibility broken)", code)
	}
}

// LEGITIMATE ADDITIONAL IdP. Tenant Y explicitly lists idp-B in allowed_issuers,
// so an idp-B token for Y is admitted. This is the feature the binding enables:
// an additional IdP authenticates a tenant only once that tenant has opted in.
func TestIssuerBinding_ListedAdditionalIdPIsAdmitted(t *testing.T) {
	srv := newMultiIDPServer(t)
	y := activeTenant("t-globex", "ext-globex", "globex")
	y.AllowedIssuers = []string{idpBIss}
	srv.SetTenants(newFakeTenants(y))

	token := mintIssuerToken(t, idpBIss, idpBAud, idpBSecret, "ext-globex")

	if code := getArtifacts(t, srv, token); code != http.StatusOK {
		t.Fatalf("idp-B token for a tenant that lists idp-B = %d, want 200 "+
			"(a legitimately bound additional IdP was refused)", code)
	}
}

// THE CROSS CASE. Tenant X lists no additional issuer; an idp-B token claiming X
// is refused — the same shape as the exploit, asserted explicitly so the binding
// is proven to discriminate by tenant, not merely to reject idp-B everywhere.
func TestIssuerBinding_UnlistedIdPForThatTenantIsRefused(t *testing.T) {
	srv := newMultiIDPServer(t)
	srv.SetTenants(newFakeTenants(
		activeTenant("t-acme", "ext-acme", "acme"), // allows only the default IdP
		func() *domain.Tenant {
			y := activeTenant("t-globex", "ext-globex", "globex")
			y.AllowedIssuers = []string{idpBIss} // allows idp-B
			return y
		}(),
	))

	// idp-B is admitted for Y (previous test) but refused for X in the same
	// server: the decision is per-tenant.
	if code := getArtifacts(t, srv, mintIssuerToken(t, idpBIss, idpBAud, idpBSecret, "ext-acme")); code != http.StatusForbidden {
		t.Fatalf("idp-B token claiming X (which does not allow idp-B) = %d, want 403", code)
	}
	if code := getArtifacts(t, srv, mintIssuerToken(t, idpBIss, idpBAud, idpBSecret, "ext-globex")); code != http.StatusOK {
		t.Fatalf("idp-B token claiming Y (which allows idp-B) = %d, want 200", code)
	}
}

// The default IdP authenticates the tenants it always did, even in a multi-IdP
// deployment: default-issuer token for an empty-binding tenant is admitted, and
// the same token for a tenant that bound ONLY idp-B is refused (the default is
// not implicitly allowed once a tenant opts into an explicit set — the operator
// must list every issuer, including the default, when they narrow it).
func TestIssuerBinding_ExplicitSetDoesNotImplicitlyIncludeDefault(t *testing.T) {
	srv := newMultiIDPServer(t)
	y := activeTenant("t-globex", "ext-globex", "globex")
	y.AllowedIssuers = []string{idpBIss} // idp-B only; default NOT listed
	srv.SetTenants(newFakeTenants(y))

	// A default-IdP token for Y is refused, because Y's explicit set is idp-B only.
	if code := getArtifacts(t, srv, mintIssuerToken(t, tIss, tAud, tSecret, "ext-globex")); code != http.StatusForbidden {
		t.Fatalf("default-IdP token for a tenant bound to idp-B only = %d, want 403", code)
	}
}
