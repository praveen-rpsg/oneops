package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/domain"
)

// ---- fake repository --------------------------------------------------------

// fakeInvitations mirrors InvitationStore's observable contract closely enough
// to pin the handler's own behaviour: which org/tenant a create actually
// lands under, and that revoke's status predicate (not a delete) is what makes
// a second call safe.
type fakeInvitations struct {
	byID map[string]*domain.Invitation

	createErr        error
	lastCreateOrg    string
	lastCreateTenant string
	lastListOrg      string
	revokeCalls      int
}

func newFakeInvitations(invs ...*domain.Invitation) *fakeInvitations {
	f := &fakeInvitations{byID: map[string]*domain.Invitation{}}
	for _, i := range invs {
		f.byID[i.InvitationID] = i
	}
	return f
}

func (f *fakeInvitations) Create(_ context.Context, i *domain.Invitation) (*domain.Invitation, error) {
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.lastCreateOrg, f.lastCreateTenant = i.OrgID, i.TenantID
	cp := *i
	cp.CreatedAt = time.Now().UTC()
	f.byID[cp.InvitationID] = &cp
	return &cp, nil
}

func (f *fakeInvitations) Get(_ context.Context, id string) (*domain.Invitation, error) {
	if i, ok := f.byID[id]; ok {
		return i, nil
	}
	return nil, domain.ErrNotFound
}

func (f *fakeInvitations) ListByOrg(_ context.Context, orgID string, _ int, _ string) ([]*domain.Invitation, error) {
	f.lastListOrg = orgID
	out := make([]*domain.Invitation, 0)
	for _, i := range f.byID {
		if i.OrgID == orgID {
			out = append(out, i)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].InvitationID < out[b].InvitationID })
	return out, nil
}

// Consume is never exercised by this story (E-ID.4b, the redeem path, is
// separate) but must exist to satisfy domain.InvitationRepository.
func (f *fakeInvitations) Consume(context.Context, string) (*domain.Invitation, error) {
	return nil, domain.ErrTokenNotRedeemable
}

func (f *fakeInvitations) Revoke(_ context.Context, id string) (*domain.Invitation, error) {
	f.revokeCalls++
	i, ok := f.byID[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	if i.Status != domain.InvitationPending {
		return nil, domain.ErrConflict
	}
	cp := *i
	cp.Status = domain.InvitationRevoked
	f.byID[id] = &cp
	return &cp, nil
}

var _ domain.InvitationRepository = (*fakeInvitations)(nil)

func seedInvitation(t *testing.T, orgID, tenantID, email string) *domain.Invitation {
	t.Helper()
	i, _, err := domain.NewInvitation(orgID, tenantID, email, invitationTTL, time.Now())
	if err != nil {
		t.Fatalf("seed invitation: %v", err)
	}
	i.CreatedAt = time.Now().UTC()
	return i
}

func decodeInvitation(t *testing.T, rec *httptest.ResponseRecorder) invitationDTO {
	t.Helper()
	var i invitationDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &i); err != nil {
		t.Fatalf("decode invitation: %v (body %s)", err, rec.Body.String())
	}
	return i
}

func decodeInvitationList(t *testing.T, rec *httptest.ResponseRecorder) []invitationDTO {
	t.Helper()
	var page struct {
		Items []invitationDTO `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode invitation list: %v (body %s)", err, rec.Body.String())
	}
	return page.Items
}

// ---- not configured ---------------------------------------------------------

func TestInvitations_UnconfiguredInvitationRegistryIsNotImplemented(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	org := seedOrg(t, "acme", "Acme")
	org.TenantID = "t-acme"
	srv.SetOrganizations(newFakeOrganizations(org))
	// SetInvitations deliberately never called.

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/invitations", ""},
		{http.MethodPost, "/v1/admin/invitations", `{"email":"a@example.com"}`},
		{http.MethodDelete, "/v1/admin/invitations/inv_1", ""},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s: got %d, want 501: %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

func TestInvitations_UnconfiguredOrganizationRegistryIsNotImplemented(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	srv.SetInvitations(newFakeInvitations())
	// SetOrganizations deliberately never called: create/list both need it to
	// resolve the caller's own org.

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/invitations", ""},
		{http.MethodPost, "/v1/admin/invitations", `{"email":"a@example.com"}`},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s: got %d, want 501: %s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

// ---- authorization ----------------------------------------------------------

// Every refusal asserts the STORE WAS NEVER REACHED, not merely the status
// code: authorization that runs after the write is not authorization.
func TestInvitations_Authorization(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/admin/invitations", ""},
		{http.MethodPost, "/v1/admin/invitations", `{"email":"a@example.com"}`},
		{http.MethodDelete, "/v1/admin/invitations/inv_1", ""},
	}
	cases := []struct {
		name  string
		token func(t *testing.T) string
		want  int
	}{
		{"no token", func(*testing.T) string { return "" }, http.StatusUnauthorized},
		{"read-only principal", func(t *testing.T) string {
			return mintRoleTenantToken(t, "ext-acme", []string{"oneops-reader"})
		}, http.StatusForbidden},
	}

	for _, tc := range cases {
		for _, rt := range routes {
			t.Run(tc.name+" "+rt.method, func(t *testing.T) {
				srv, _ := newTestServer(true)
				srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
				org := seedOrg(t, "acme", "Acme")
				org.TenantID = "t-acme"
				srv.SetOrganizations(newFakeOrganizations(org))
				spy := newFakeInvitations()
				srv.SetInvitations(spy)

				req := httptest.NewRequest(rt.method, rt.path, strings.NewReader(rt.body))
				if tok := tc.token(t); tok != "" {
					req.Header.Set("Authorization", "Bearer "+tok)
				}
				rec := httptest.NewRecorder()
				srv.Router().ServeHTTP(rec, req)

				if rec.Code != tc.want {
					t.Errorf("%s %s = %d, want %d: %s", rt.method, rt.path, rec.Code, tc.want, rec.Body.String())
				}
				if spy.lastCreateOrg != "" || spy.lastListOrg != "" || spy.revokeCalls != 0 {
					t.Errorf("%s %s: the store was reached despite refusal", rt.method, rt.path)
				}
			})
		}
	}
}

// The tenant administrator succeeds, so the refusals above are about
// authorization and not about a route that never works.
func TestInvitations_TenantAdminIsAdmitted(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	org := seedOrg(t, "acme", "Acme")
	org.TenantID = "t-acme"
	srv.SetOrganizations(newFakeOrganizations(org))
	srv.SetInvitations(newFakeInvitations())

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/invitations",
		strings.NewReader(`{"email":"person@example.com"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("tenant administrator = %d, want 201: %s", rec.Code, rec.Body.String())
	}
}

// ---- create -----------------------------------------------------------------

// TestInvitations_CreateResolvesOrgFromContext is the isolation assertion this
// story exists to prove: the invitation lands in the CALLER's own org/tenant,
// resolved from the authenticated context — never anything the request body
// could name, because createInvitationRequest has no such field to smuggle
// one through.
func TestInvitations_CreateResolvesOrgFromContext(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	org := seedOrg(t, "acme", "Acme")
	org.TenantID = "t-acme"
	repo := newFakeOrganizations(org)
	srv.SetOrganizations(repo)
	spy := newFakeInvitations()
	srv.SetInvitations(spy)

	// The body attempts to smuggle an org_id/tenant_id; createInvitationRequest
	// has no field for either, so JSON decoding silently discards them.
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/invitations",
		strings.NewReader(`{"email":"person@example.com","org_id":"org_attacker","tenant_id":"t-victim"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201: %s", rec.Code, rec.Body.String())
	}
	if spy.lastCreateOrg != org.OrgID {
		t.Errorf("invitation created under org_id %q, want the caller's own %q (not the smuggled org_attacker)",
			spy.lastCreateOrg, org.OrgID)
	}
	if spy.lastCreateTenant != "t-acme" {
		t.Errorf("invitation created under tenant_id %q, want the caller's own %q (not the smuggled t-victim)",
			spy.lastCreateTenant, "t-acme")
	}

	var resp createInvitationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode create response: %v (body %s)", err, rec.Body.String())
	}
	if resp.OrgID != org.OrgID {
		t.Errorf("response org_id = %q, want the caller's own %q", resp.OrgID, org.OrgID)
	}
	if resp.OrgID == "org_attacker" {
		t.Error("the response's org_id was taken from the request body")
	}
}

// TestInvitations_CreateReturnsTokenOnce proves the one-time token property:
// non-empty on create, absent everywhere else.
func TestInvitations_CreateReturnsTokenOnce(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	org := seedOrg(t, "acme", "Acme")
	org.TenantID = "t-acme"
	srv.SetOrganizations(newFakeOrganizations(org))
	spy := newFakeInvitations()
	srv.SetInvitations(spy)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/invitations",
		strings.NewReader(`{"email":"person@example.com"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	req.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(createRec, req)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201: %s", createRec.Code, createRec.Body.String())
	}
	var created createInvitationResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Token == "" {
		t.Fatal("create response carries no token")
	}
	if strings.Contains(createRec.Body.String(), "token_hash") {
		t.Error("create response leaked token_hash")
	}

	// A subsequent list must never expose the token or its hash.
	listReq := httptest.NewRequest(http.MethodGet, "/v1/admin/invitations", nil)
	listReq.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	listRec := httptest.NewRecorder()
	srv.Router().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list: got %d, want 200: %s", listRec.Code, listRec.Body.String())
	}
	body := listRec.Body.String()
	if strings.Contains(body, created.Token) {
		t.Error("the plaintext token leaked into the list response")
	}
	if strings.Contains(body, `"token"`) || strings.Contains(body, "token_hash") {
		t.Errorf("list response carries a token field: %s", body)
	}
}

func TestInvitations_CreateRejectsInvalidBody(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	org := seedOrg(t, "acme", "Acme")
	org.TenantID = "t-acme"
	srv.SetOrganizations(newFakeOrganizations(org))
	spy := newFakeInvitations()
	srv.SetInvitations(spy)

	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"malformed json", `{"email":`, http.StatusBadRequest},
		{"empty email", `{"email":""}`, http.StatusUnprocessableEntity},
		{"blank email", `{"email":"   "}`, http.StatusUnprocessableEntity},
		{"missing @", `{"email":"not-an-email"}`, http.StatusUnprocessableEntity},
		{"too long", `{"email":"` + strings.Repeat("a", 250) + `@b.com"}`, http.StatusUnprocessableEntity},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/admin/invitations", strings.NewReader(c.body))
			req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("got %d, want %d (body %s)", rec.Code, c.want, rec.Body.String())
			}
		})
	}
	if spy.lastCreateOrg != "" {
		t.Error("an invalid request reached the invitation store")
	}
}

func TestInvitations_CreateNoOrgForTenantIsNotFound(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	srv.SetOrganizations(newFakeOrganizations()) // no organization for t-acme
	srv.SetInvitations(newFakeInvitations())

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/invitations",
		strings.NewReader(`{"email":"person@example.com"}`))
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

// ---- list -------------------------------------------------------------------

func TestInvitations_ListResolvesOrgFromContext(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(
		activeTenant("t-acme", "ext-acme", "acme"),
		activeTenant("t-beta", "ext-beta", "beta"),
	))
	orgAcme := seedOrg(t, "acme-org", "Acme Org")
	orgAcme.TenantID = "t-acme"
	orgBeta := seedOrg(t, "beta-org", "Beta Org")
	orgBeta.TenantID = "t-beta"
	srv.SetOrganizations(newFakeOrganizations(orgAcme, orgBeta))

	invAcme := seedInvitation(t, orgAcme.OrgID, "t-acme", "acme-invitee@example.com")
	invBeta := seedInvitation(t, orgBeta.OrgID, "t-beta", "beta-invitee@example.com")
	srv.SetInvitations(newFakeInvitations(invAcme, invBeta))

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/invitations", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := decodeInvitationList(t, rec)
	if len(got) != 1 || got[0].InvitationID != invAcme.InvitationID {
		t.Fatalf("list returned %+v, want exactly acme's own invitation", got)
	}
}

func TestInvitations_ListEmptySerializesAsArray(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	org := seedOrg(t, "acme", "Acme")
	org.TenantID = "t-acme"
	srv.SetOrganizations(newFakeOrganizations(org))
	srv.SetInvitations(newFakeInvitations())

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/invitations", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("body = %s, want items rendered as [], not null", rec.Body.String())
	}
}

// ---- revoke -----------------------------------------------------------------

func TestInvitations_RevokeSucceeds(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	org := seedOrg(t, "acme", "Acme")
	org.TenantID = "t-acme"
	srv.SetOrganizations(newFakeOrganizations(org))
	inv := seedInvitation(t, org.OrgID, "t-acme", "person@example.com")
	srv.SetInvitations(newFakeInvitations(inv))

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/invitations/"+inv.InvitationID, nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := decodeInvitation(t, rec)
	if got.Status != string(domain.InvitationRevoked) {
		t.Errorf("status = %q, want revoked", got.Status)
	}
}

// Revoke is idempotent-safe: a second call against an already-revoked
// invitation is a conflict (409), never a silent success or a panic.
func TestInvitations_RevokeTwiceIsConflict(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	org := seedOrg(t, "acme", "Acme")
	org.TenantID = "t-acme"
	srv.SetOrganizations(newFakeOrganizations(org))
	inv := seedInvitation(t, org.OrgID, "t-acme", "person@example.com")
	srv.SetInvitations(newFakeInvitations(inv))

	revoke := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodDelete, "/v1/admin/invitations/"+inv.InvitationID, nil)
		req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}

	first := revoke()
	if first.Code != http.StatusOK {
		t.Fatalf("first revoke: got %d, want 200: %s", first.Code, first.Body.String())
	}
	second := revoke()
	if second.Code != http.StatusConflict {
		t.Fatalf("second revoke: got %d, want 409: %s", second.Code, second.Body.String())
	}
}

func TestInvitations_RevokeUnknownIsNotFound(t *testing.T) {
	srv, _ := newTestServer(true)
	srv.SetTenants(newFakeTenants(activeTenant("t-acme", "ext-acme", "acme")))
	org := seedOrg(t, "acme", "Acme")
	org.TenantID = "t-acme"
	srv.SetOrganizations(newFakeOrganizations(org))
	srv.SetInvitations(newFakeInvitations())

	req := httptest.NewRequest(http.MethodDelete, "/v1/admin/invitations/inv_missing", nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, "ext-acme", []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404: %s", rec.Code, rec.Body.String())
	}
}
