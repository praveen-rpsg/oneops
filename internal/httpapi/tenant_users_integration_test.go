//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
	"github.com/rpsg/oneops/internal/store/migrate"
	"github.com/rpsg/oneops/internal/store/postgres"
)

// tenantUsersHarness wires GET /admin/tenant-users against the REAL
// MembershipStore — a tenant-scoped appPool (row-level security enforced,
// exactly as production's composition root builds it) plus a privileged
// pool used ONLY to register tenants/organizations/users, never for
// anything the directory endpoint itself reads. This is the wiring the
// handler unit tests (fakes) cannot prove: that RLS, not a predicate the
// query happens to carry, is what isolates one tenant's membership rows
// (and therefore its directory) from another's (ADR-ACT-003).
type tenantUsersHarness struct {
	router      http.Handler
	dsn         string
	priv        *pgxpool.Pool
	appPool     *pgxpool.Pool
	tenants     *postgres.TenantStore
	users       *postgres.UserStore
	memberships *postgres.MembershipStore
}

func realTenantUsersHarness(t *testing.T) *tenantUsersHarness {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	dsn := base + sep + "options=-c%20search_path%3Dhttpapi_itest"
	ctx := context.Background()

	priv, err := postgres.NewPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("privileged pool: %v", err)
	}
	var pingErr error
	for i := 0; i < 60; i++ {
		if pingErr = priv.Ping(ctx); pingErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if pingErr != nil {
		t.Fatalf("db not ready: %v", pingErr)
	}
	if _, err := priv.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS httpapi_itest`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if err := migrate.Up(ctx, priv); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(priv.Close)

	appPool, err := postgres.NewTenantScopedPool(ctx, dsn, 5)
	if err != nil {
		t.Fatalf("tenant-scoped pool: %v", err)
	}
	t.Cleanup(appPool.Close)

	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), priv.Ping)

	h := &tenantUsersHarness{
		dsn: dsn, priv: priv, appPool: appPool,
		tenants:     postgres.NewTenantStore(priv),
		users:       postgres.NewUserStore(priv),
		memberships: postgres.NewMembershipStore(appPool),
	}
	s.SetTenants(h.tenants)
	s.SetTenantUserDirectory(h.memberships)
	h.router = s.Router()
	return h
}

// tenantUsersTenant registers a real tenant on the privileged pool, exactly
// as asset_graph_integration_test.go's assetGraphTenant does.
func (h *tenantUsersHarness) tenantUsersTenant(t *testing.T, slug string) *domain.Tenant {
	t.Helper()
	slug = uniqueSlug(slug)
	tn, err := h.tenants.Create(domain.WithActor(context.Background(), "test-actor"), &domain.Tenant{
		TenantID: domain.NewID(), Slug: slug, Name: slug, ExternalID: "ext-" + slug,
		Status: domain.TenantActive,
	})
	if err != nil {
		t.Fatalf("create tenant %s: %v", slug, err)
	}
	return tn
}

// createOrgFor inserts a real organization row for an already-registered
// tenant, directly via the privileged pool rather than
// OrganizationStore.Create: that constructor mints its OWN tenant row
// (domain.NewOrganization always generates a fresh TenantID), which would
// collide with the tenant this harness already registered with a resolvable
// ExternalID for JWT minting. membership.org_id's only requirement is a real
// organization row naming the same tenant_id — a direct insert of that one
// row is the minimal fixture, the same class of direct-SQL fixture setup
// noc_overview_integration_test.go's addRosterUser already uses for
// on_call_participant.
func (h *tenantUsersHarness) createOrgFor(t *testing.T, tn *domain.Tenant) string {
	t.Helper()
	orgID := domain.NewID()
	if _, err := h.priv.Exec(context.Background(), `
		INSERT INTO organization (org_id, tenant_id, slug, name, status)
		VALUES ($1, $2, $3, $3, 'active')`,
		orgID, tn.TenantID, uniqueSlug("org")); err != nil {
		t.Fatalf("create organization for tenant %s: %v", tn.TenantID, err)
	}
	return orgID
}

// createUser inserts a real app_user row with the given status, directly
// through UserStore.Create (which persists whatever status the struct
// carries — the invited-only default lives in domain.NewUser, a
// constructor this fixture deliberately bypasses so a suspended account can
// be seeded without an extra transition call).
func (h *tenantUsersHarness) createUser(t *testing.T, email, displayName string, status domain.UserStatus) *domain.User {
	t.Helper()
	u := &domain.User{
		UserID: domain.NewID(), Email: domain.NormalizeEmail(email),
		DisplayName: displayName, Status: status,
	}
	created, err := h.users.Create(domain.WithActor(context.Background(), "test-actor"), u)
	if err != nil {
		t.Fatalf("create user %s: %v", email, err)
	}
	return created
}

// grantActive grants userID an ACTIVE membership of orgID inside tn, through
// the real MembershipStore.Grant write path (audited, RLS-write-checked).
func (h *tenantUsersHarness) grantActive(t *testing.T, tn *domain.Tenant, orgID, userID string) {
	t.Helper()
	m, err := domain.NewMembership(orgID, tn.TenantID, userID)
	if err != nil {
		t.Fatalf("new membership: %v", err)
	}
	ctx := domain.WithActor(domain.WithTenant(context.Background(), tn), "test-actor")
	if _, err := h.memberships.Grant(ctx, m); err != nil {
		t.Fatalf("grant membership for %s in %s: %v", userID, tn.TenantID, err)
	}
}

// revoke revokes membershipID inside tn.
func (h *tenantUsersHarness) revoke(t *testing.T, tn *domain.Tenant, membershipID string) {
	t.Helper()
	ctx := domain.WithActor(domain.WithTenant(context.Background(), tn), "test-actor")
	if _, err := h.memberships.Revoke(ctx, membershipID); err != nil {
		t.Fatalf("revoke membership %s: %v", membershipID, err)
	}
}

// grantActiveReturningMembershipID is grantActive, but returns the minted
// membership_id so a caller can revoke it afterwards.
func (h *tenantUsersHarness) grantActiveReturningMembershipID(t *testing.T, tn *domain.Tenant, orgID, userID string) string {
	t.Helper()
	m, err := domain.NewMembership(orgID, tn.TenantID, userID)
	if err != nil {
		t.Fatalf("new membership: %v", err)
	}
	ctx := domain.WithActor(domain.WithTenant(context.Background(), tn), "test-actor")
	granted, err := h.memberships.Grant(ctx, m)
	if err != nil {
		t.Fatalf("grant membership for %s in %s: %v", userID, tn.TenantID, err)
	}
	return granted.MembershipID
}

// tenantUsersGet issues an authenticated GET /v1/admin/tenant-users as tn's
// admin, against whatever router r serves (so the "bites" test can swap in a
// deliberately loosened one).
func tenantUsersGet(t *testing.T, r http.Handler, tn *domain.Tenant, query string) (*httptest.ResponseRecorder, []tenantUserDTO) {
	t.Helper()
	path := "/v1/admin/tenant-users"
	if query != "" {
		path += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+mintRoleTenantToken(t, tn.ExternalID, []string{"oneops-admin"}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var got struct {
		Items []tenantUserDTO `json:"items"`
	}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode directory: %v (body %s)", err, rec.Body.String())
		}
	}
	return rec, got.Items
}

func (h *tenantUsersHarness) get(t *testing.T, tn *domain.Tenant, query string) (*httptest.ResponseRecorder, []tenantUserDTO) {
	return tenantUsersGet(t, h.router, tn, query)
}

// maxTenantUsersPagesToWalk bounds tenantUsersFindAcrossAllPages's loop. At
// maxMembershipPageSize (500) rows per page this tolerates a 500,000-row
// accumulated directory before treating a still-unexhausted walk as a bug in
// the paging loop itself rather than legitimate data — comfortably above
// anything this suite's own fixtures could ever produce.
const maxTenantUsersPagesToWalk = 1000

// tenantUsersFindAcrossAllPages walks EVERY page of GET /admin/tenant-users
// (as tn's admin, against router r) via the keyset `after` cursor, and
// reports whether wantUserID appears anywhere in the whole collection —
// not just the first page.
//
// This is the fix for a test-robustness defect (not a production one): the
// mutation canary below runs against a PRIVILEGED, unscoped MembershipStore
// on purpose, so it correctly returns EVERY tenant's active members ever
// created against this suite's persistent dev database, not just this run's
// fixture. user_id is a ULID, so a freshly-created leaked row sorts near the
// END of that accumulated set once the database holds more than one page's
// worth of history — a first-page-only presence check is therefore only
// reliable against a database freshly reset for this test, which this
// harness's shared, non-truncating schema deliberately is not (the same
// class of non-idempotency TestIncidentTrendsAPI_TenantIsolation_BitesWhenLoosened's
// own presence-based, not exact-count, check exists to tolerate). Walking
// every page finds the target wherever it landed, regardless of how much
// prior run history the database has accumulated.
func tenantUsersFindAcrossAllPages(t *testing.T, r http.Handler, tn *domain.Tenant, wantUserID string) (found bool, pagesWalked int) {
	t.Helper()
	after := ""
	for pagesWalked = 0; pagesWalked < maxTenantUsersPagesToWalk; pagesWalked++ {
		q := "limit=500"
		if after != "" {
			q += "&after=" + after
		}
		rec, items := tenantUsersGet(t, r, tn, q)
		if rec.Code != http.StatusOK {
			t.Fatalf("page %d: status = %d, want 200: %s", pagesWalked, rec.Code, rec.Body.String())
		}
		for _, it := range items {
			if it.UserID == wantUserID {
				return true, pagesWalked + 1
			}
		}
		if len(items) < 500 {
			// A short page is the last one: the collection is exhausted.
			return false, pagesWalked + 1
		}
		after = items[len(items)-1].UserID
	}
	t.Fatalf("walked %d pages without exhausting the collection or finding %q — "+
		"either the paging loop is not advancing, or the accumulated fixture data has grown "+
		"implausibly large", maxTenantUsersPagesToWalk, wantUserID)
	return false, pagesWalked
}

func userIDs(items []tenantUserDTO) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.UserID)
	}
	sort.Strings(out)
	return out
}

// TestTenantUsersAPI_ReturnsExactlyActiveMembers is the correctness proof
// the review criteria call for: N active members, a member whose global
// account has since been suspended, and a member whose MEMBERSHIP has been
// revoked all get seeded in the same tenant — only the N active members
// come back, in ascending user_id order.
func TestTenantUsersAPI_ReturnsExactlyActiveMembers(t *testing.T) {
	h := realTenantUsersHarness(t)
	tn := h.tenantUsersTenant(t, "tenant-users-correctness")
	orgID := h.createOrgFor(t, tn)

	var wantIDs []string
	for i := 0; i < 3; i++ {
		u := h.createUser(t, uniqueSlug("active")+"@example.com", "Active User", domain.UserActive)
		h.grantActive(t, tn, orgID, u.UserID)
		wantIDs = append(wantIDs, u.UserID)
	}

	// A member whose global account is suspended: an active membership row
	// naming a user who can no longer act. Must NOT appear — see
	// MembershipStore.ListActiveDirectory's own doc comment for why "ACTIVE
	// member" requires both halves.
	suspended := h.createUser(t, uniqueSlug("suspended")+"@example.com", "Suspended User", domain.UserSuspended)
	h.grantActive(t, tn, orgID, suspended.UserID)

	// A member whose MEMBERSHIP itself has been revoked, account otherwise
	// active. Must NOT appear.
	revokedUser := h.createUser(t, uniqueSlug("revoked")+"@example.com", "Revoked User", domain.UserActive)
	revokedMembershipID := h.grantActiveReturningMembershipID(t, tn, orgID, revokedUser.UserID)
	h.revoke(t, tn, revokedMembershipID)

	rec, items := h.get(t, tn, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(items) != 3 {
		t.Fatalf("items = %d, want 3 (only the active members): %+v", len(items), items)
	}

	got := userIDs(items)
	sort.Strings(wantIDs)
	for i := range wantIDs {
		if got[i] != wantIDs[i] {
			t.Errorf("item[%d] user_id = %q, want %q (got=%v want=%v)", i, got[i], wantIDs[i], got, wantIDs)
		}
	}
	for _, it := range items {
		if it.UserID == suspended.UserID {
			t.Error("the suspended user's account appears in the directory")
		}
		if it.UserID == revokedUser.UserID {
			t.Error("the user with a revoked membership appears in the directory")
		}
		if it.Email == "" || it.DisplayName == "" {
			t.Errorf("item %+v is missing profile fields the app_user join should supply", it)
		}
	}

	// Ascending user_id order (the query's own ORDER BY), so a repeated call
	// against unchanged data is stable and pagination is deterministic.
	for i := 1; i < len(items); i++ {
		if items[i-1].UserID >= items[i].UserID {
			t.Errorf("items are not in ascending user_id order: %v", userIDs(items))
			break
		}
	}
}

// TestTenantUsersAPI_EmptyTenantIsCleanEmpty proves the empty-tenant bound:
// a brand-new tenant with no memberships at all gets 200 with an empty
// (never null) items array.
func TestTenantUsersAPI_EmptyTenantIsCleanEmpty(t *testing.T) {
	h := realTenantUsersHarness(t)
	tn := h.tenantUsersTenant(t, "tenant-users-empty")

	rec, items := h.get(t, tn, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if len(items) != 0 {
		t.Errorf("items = %+v, want empty", items)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("body = %s, want items rendered as [], not null", rec.Body.String())
	}
}

// TestTenantUsersAPI_TenantIsolation is the security-critical property this
// story exists to prove: two tenants, each with their own active members,
// PLUS a user who is a member of tenant B only, sharing the global app_user
// table with tenant A's own members. Tenant A's directory must contain
// EXACTLY its own members — never B's, and never the shared-table bystander
// who never joined A.
func TestTenantUsersAPI_TenantIsolation(t *testing.T) {
	h := realTenantUsersHarness(t)
	tnA := h.tenantUsersTenant(t, "tenant-users-iso-a")
	tnB := h.tenantUsersTenant(t, "tenant-users-iso-b")
	orgA := h.createOrgFor(t, tnA)
	orgB := h.createOrgFor(t, tnB)

	a1 := h.createUser(t, uniqueSlug("a1")+"@example.com", "A One", domain.UserActive)
	a2 := h.createUser(t, uniqueSlug("a2")+"@example.com", "A Two", domain.UserActive)
	h.grantActive(t, tnA, orgA, a1.UserID)
	h.grantActive(t, tnA, orgA, a2.UserID)

	b1 := h.createUser(t, uniqueSlug("b1")+"@example.com", "B One", domain.UserActive)
	b2 := h.createUser(t, uniqueSlug("b2")+"@example.com", "B Two", domain.UserActive)
	b3 := h.createUser(t, uniqueSlug("b3")+"@example.com", "B Three", domain.UserActive)
	h.grantActive(t, tnB, orgB, b1.UserID)
	h.grantActive(t, tnB, orgB, b2.UserID)
	h.grantActive(t, tnB, orgB, b3.UserID)

	_, gotA := h.get(t, tnA, "")
	_, gotB := h.get(t, tnB, "")

	if len(gotA) != 2 {
		t.Fatalf("tenant A directory = %+v, want 2 entries", gotA)
	}
	if len(gotB) != 3 {
		t.Fatalf("tenant B directory = %+v, want 3 entries", gotB)
	}

	idsA := userIDs(gotA)
	for _, leak := range []string{b1.UserID, b2.UserID, b3.UserID} {
		for _, id := range idsA {
			if id == leak {
				t.Errorf("tenant A's directory leaked tenant B's member %q", leak)
			}
		}
	}
	idsB := userIDs(gotB)
	for _, leak := range []string{a1.UserID, a2.UserID} {
		for _, id := range idsB {
			if id == leak {
				t.Errorf("tenant B's directory leaked tenant A's member %q", leak)
			}
		}
	}
}

// TestTenantUsersAPI_TenantIsolation_BitesWhenLoosened is the mutation proof
// the review criteria require: re-run the isolation fixture and assertions
// against a router deliberately wired with MembershipStore built over the
// PRIVILEGED (RLS-bypassing) pool instead of the tenant-scoped one, and
// require the isolation assertion to FAIL there. If it did not fail, the
// "real" test above would be proving nothing.
func TestTenantUsersAPI_TenantIsolation_BitesWhenLoosened(t *testing.T) {
	h := realTenantUsersHarness(t)
	tnA := h.tenantUsersTenant(t, "tenant-users-bites-a")
	tnB := h.tenantUsersTenant(t, "tenant-users-bites-b")
	orgA := h.createOrgFor(t, tnA)
	orgB := h.createOrgFor(t, tnB)

	h.grantActive(t, tnA, orgA, h.createUser(t, uniqueSlug("bites-a")+"@example.com", "Bites A", domain.UserActive).UserID)
	bB := h.createUser(t, uniqueSlug("bites-b")+"@example.com", "Bites B", domain.UserActive)
	h.grantActive(t, tnB, orgB, bB.UserID)

	// Wire a SECOND server whose MembershipStore is built over the privileged
	// pool (postgres.NewPool, table-owner/BYPASSRLS — no app.tenant_id binding
	// at all) — the exact mistake ADR-TENANCY-002 exists to catch.
	loosePool, err := postgres.NewPool(context.Background(), h.dsn, 3)
	if err != nil {
		t.Fatalf("privileged pool for loosened router: %v", err)
	}
	t.Cleanup(loosePool.Close)

	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	loose := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), loosePool.Ping)
	loose.SetTenants(h.tenants)
	loose.SetTenantUserDirectory(postgres.NewMembershipStore(loosePool))
	looseRouter := loose.Router()

	// Walk EVERY page, not just the first: the loosened router returns every
	// tenant's active members ever created against this suite's persistent
	// dev database, and bB's freshly-minted ULID can sort well past page one
	// once that accumulated history exceeds it — see
	// tenantUsersFindAcrossAllPages's own doc comment.
	leaked, pagesWalked := tenantUsersFindAcrossAllPages(t, looseRouter, tnA, bB.UserID)
	if !leaked {
		t.Fatalf("expected the LOOSENED router (privileged pool, no tenant scoping) to leak tenant B's "+
			"member into tenant A's directory across %d page(s); it did not, which means this canary no "+
			"longer proves the real harness's isolation is load-bearing", pagesWalked)
	}
}

// TestTenantUsersAPI_Pagination proves the bound is real, not merely
// documented: with limit=1, three active members come back one page at a
// time, in the same ascending user_id order, and a repeated cursor never
// repeats or skips a row.
func TestTenantUsersAPI_Pagination(t *testing.T) {
	h := realTenantUsersHarness(t)
	tn := h.tenantUsersTenant(t, "tenant-users-page")
	orgID := h.createOrgFor(t, tn)

	var ids []string
	for i := 0; i < 3; i++ {
		u := h.createUser(t, uniqueSlug("page")+"@example.com", "Page User", domain.UserActive)
		h.grantActive(t, tn, orgID, u.UserID)
		ids = append(ids, u.UserID)
	}
	sort.Strings(ids)

	var paged []string
	after := ""
	for i := 0; i < 3; i++ {
		q := "limit=1"
		if after != "" {
			q += "&after=" + after
		}
		rec, items := h.get(t, tn, q)
		if rec.Code != http.StatusOK {
			t.Fatalf("page %d: status = %d, want 200: %s", i, rec.Code, rec.Body.String())
		}
		if len(items) != 1 {
			t.Fatalf("page %d: items = %+v, want exactly 1 (limit=1)", i, items)
		}
		paged = append(paged, items[0].UserID)
		after = items[0].UserID
	}
	if len(paged) != len(ids) {
		t.Fatalf("paged %v, want %v", paged, ids)
	}
	for i := range ids {
		if paged[i] != ids[i] {
			t.Errorf("page[%d] = %q, want %q (paged=%v want=%v)", i, paged[i], ids[i], paged, ids)
		}
	}

	// One more page past the end is empty, not an error.
	_, tail := h.get(t, tn, "limit=1&after="+after)
	if len(tail) != 0 {
		t.Errorf("page past the end = %+v, want empty", tail)
	}
}
