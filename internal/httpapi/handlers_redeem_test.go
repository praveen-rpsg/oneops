package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// ---- fake repository --------------------------------------------------------

// fakeRedemptions mirrors RedemptionRepository's observable contract closely
// enough to pin the handler's own behaviour: which token it forwarded, and
// that every refusal — whatever the underlying cause — reaches the handler as
// the same domain.ErrTokenNotRedeemable.
type fakeRedemptions struct {
	byToken map[string]*domain.Redemption
	err     error

	calls           int
	lastToken       string
	lastDisplayName string
}

func newFakeRedemptions() *fakeRedemptions {
	return &fakeRedemptions{byToken: map[string]*domain.Redemption{}}
}

func (f *fakeRedemptions) Redeem(_ context.Context, token, displayName string) (*domain.Redemption, error) {
	f.calls++
	f.lastToken = token
	f.lastDisplayName = displayName
	if f.err != nil {
		return nil, f.err
	}
	r, ok := f.byToken[token]
	if !ok {
		return nil, domain.ErrTokenNotRedeemable
	}
	return r, nil
}

var _ domain.RedemptionRepository = (*fakeRedemptions)(nil)

func seedRedemption(t *testing.T, orgID, tenantID, email string) (*domain.Redemption, string) {
	t.Helper()
	inv, token, err := domain.NewInvitation(orgID, tenantID, email, invitationTTL, time.Now())
	if err != nil {
		t.Fatalf("seed invitation: %v", err)
	}
	redeemedAt := time.Now().UTC()
	inv.Status = domain.InvitationRedeemed
	inv.RedeemedAt = &redeemedAt

	u, err := domain.NewUser(email, "")
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	u.Status = domain.UserActive

	m, err := domain.NewMembership(orgID, tenantID, u.UserID)
	if err != nil {
		t.Fatalf("seed membership: %v", err)
	}

	return &domain.Redemption{
		Invitation:        inv,
		User:              u,
		Membership:        m,
		UserCreated:       true,
		MembershipCreated: true,
	}, token
}

// ---- not configured ---------------------------------------------------------

func TestRedeemInvitation_UnconfiguredIsNotImplemented(t *testing.T) {
	srv, _ := newTestServer(true)
	// SetRedemptions deliberately never called.

	req := httptest.NewRequest(http.MethodPost, "/auth/invitations/redeem",
		strings.NewReader(`{"token":"whatever"}`))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("got %d, want 501: %s", rec.Code, rec.Body.String())
	}
}

// ---- unauthenticated by design -----------------------------------------------

// TestRedeemInvitation_WorksWithNoAuthorizationHeader is the point of this
// endpoint: it is unauthenticated by construction, not merely permissive about
// a missing header.
func TestRedeemInvitation_WorksWithNoAuthorizationHeader(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetOrganizations(newFakeOrganizations(orgNamed(t, "org-1", "tenant-1", "Acme Inc")))
	spy := newFakeRedemptions()
	red, token := seedRedemption(t, "org-1", "tenant-1", "invitee@example.com")
	spy.byToken[token] = red
	srv.SetRedemptions(spy)

	req := httptest.NewRequest(http.MethodPost, "/auth/invitations/redeem",
		strings.NewReader(`{"token":"`+token+`"}`))
	// No Authorization header at all.
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 with no Authorization header: %s", rec.Code, rec.Body.String())
	}
	if spy.lastToken != token {
		t.Errorf("forwarded token %q, want %q", spy.lastToken, token)
	}
}

// TestRedeemInvitation_NotUnderV1 proves the route is not reachable under the
// authenticated /v1 surface — it must not exist there under any name, because
// that group's s.authenticate would reject every caller before a handler runs.
func TestRedeemInvitation_NotUnderV1(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetRedemptions(newFakeRedemptions())

	req := httptest.NewRequest(http.MethodPost, "/v1/invitations/redeem", strings.NewReader(`{"token":"x"}`))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatalf("POST /v1/invitations/redeem unexpectedly succeeded")
	}
}

// ---- request shape: token only -----------------------------------------------

// TestRedeemInvitation_RequestCarriesOnlyToken proves that org_id, tenant_id,
// email and user_id in the body are silently discarded — redeemInvitationRequest
// has no field for any of them, so there is nothing for an attacker to smuggle.
func TestRedeemInvitation_RequestCarriesOnlyToken(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetOrganizations(newFakeOrganizations(orgNamed(t, "org-1", "tenant-1", "Acme Inc")))
	spy := newFakeRedemptions()
	red, token := seedRedemption(t, "org-1", "tenant-1", "invitee@example.com")
	spy.byToken[token] = red
	srv.SetRedemptions(spy)

	body := `{"token":"` + token + `","org_id":"org_attacker","tenant_id":"t-victim",` +
		`"email":"attacker@example.com","user_id":"user_attacker","display_name":"Attacker"}`
	req := httptest.NewRequest(http.MethodPost, "/auth/invitations/redeem", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if spy.lastToken != token {
		t.Errorf("forwarded token %q, want %q", spy.lastToken, token)
	}
	// The only thing Redeem was ever given besides the token is an empty
	// display name — never anything from the smuggled fields above.
	if spy.lastDisplayName != "" {
		t.Errorf("display name forwarded to Redeem = %q, want empty (this request has no such field)",
			spy.lastDisplayName)
	}
}

// ---- generic failure: no enumeration -----------------------------------------

// TestRedeemInvitation_EveryRefusalIsTheSameGenericResponse is the security
// property this endpoint exists to prove: an unknown token and a store-level
// refusal for any other reason (expired, revoked, redeemed, suspended,
// deactivated — all folded into ErrTokenNotRedeemable by the store) produce
// byte-identical status and body shape.
func TestRedeemInvitation_EveryRefusalIsTheSameGenericResponse(t *testing.T) {
	srv, _ := newTestServer(true)
	spy := newFakeRedemptions() // empty: every token is "unknown"
	srv.SetRedemptions(spy)

	redeem := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/auth/invitations/redeem",
			strings.NewReader(`{"token":"`+token+`"}`))
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}

	unknown := redeem("never-issued")
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown token: got %d, want 400: %s", unknown.Code, unknown.Body.String())
	}

	spy.err = domain.ErrTokenNotRedeemable
	otherCause := redeem("some-other-token")
	if otherCause.Code != unknown.Code {
		t.Fatalf("a different underlying cause produced status %d, want the same %d as an unknown token",
			otherCause.Code, unknown.Code)
	}
	if normalizeInstance(t, otherCause.Body.String()) != normalizeInstance(t, unknown.Body.String()) {
		t.Fatalf("responses differ in shape:\n  unknown:    %s\n  other cause: %s",
			unknown.Body.String(), otherCause.Body.String())
	}
}

// normalizeInstance strips the per-request instance (request id), the one
// field the RFC 7807 body legitimately varies by request, so two refusals for
// different underlying causes can be compared on everything that must not
// vary: status, title and detail.
func normalizeInstance(t *testing.T, body string) string {
	t.Helper()
	var p Problem
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("decode problem body: %v (body %s)", err, body)
	}
	p.Instance = ""
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("re-marshal problem body: %v", err)
	}
	return string(out)
}

func TestRedeemInvitation_MalformedBodyIsBadRequest(t *testing.T) {
	srv, _ := newTestServer(true)
	spy := newFakeRedemptions()
	srv.SetRedemptions(spy)

	req := httptest.NewRequest(http.MethodPost, "/auth/invitations/redeem", strings.NewReader(`{"token":`))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if spy.calls != 0 {
		t.Error("a malformed body reached the redemption store")
	}
}

// ---- success response ---------------------------------------------------------

// TestRedeemInvitation_SuccessResponseIsMinimal proves the response carries
// only the organisation's name — no user id, membership id or invitation id
// that would hand an enumeration surface to whoever holds the token.
func TestRedeemInvitation_SuccessResponseIsMinimal(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetOrganizations(newFakeOrganizations(orgNamed(t, "org-1", "tenant-1", "Acme Inc")))
	spy := newFakeRedemptions()
	red, token := seedRedemption(t, "org-1", "tenant-1", "invitee@example.com")
	spy.byToken[token] = red
	srv.SetRedemptions(spy)

	req := httptest.NewRequest(http.MethodPost, "/auth/invitations/redeem",
		strings.NewReader(`{"token":"`+token+`"}`))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
	}
	if resp["organization"] != "Acme Inc" {
		t.Errorf("organization = %v, want %q", resp["organization"], "Acme Inc")
	}
	for _, forbidden := range []string{"user_id", "membership_id", "invitation_id", "email", "token"} {
		if _, present := resp[forbidden]; present {
			t.Errorf("response carries %q, which is enumeration-fuel this endpoint must not return", forbidden)
		}
	}
}

// orgNamed builds a minimal seeded organization for the fake organization
// registry, naming both the org and its tenant so the redeem handler's
// courtesy org-name lookup can resolve it.
func orgNamed(t *testing.T, orgID, tenantID, name string) *domain.Organization {
	t.Helper()
	o, err := domain.NewOrganization(name, "slug-"+orgID)
	if err != nil {
		t.Fatalf("build organization: %v", err)
	}
	o.OrgID = orgID
	o.TenantID = tenantID
	o.RowVersion = 1
	o.CreatedAt = time.Now().UTC()
	o.UpdatedAt = o.CreatedAt
	return o
}
