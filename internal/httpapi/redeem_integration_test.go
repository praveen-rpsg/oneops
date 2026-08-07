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

// redeemHarness wires POST /auth/invitations/redeem against the REAL
// RedemptionStore, over the PRIVILEGED pool — exactly the composition
// cmd/controlplane/main.go builds it over (RedemptionRepository necessarily
// runs outside row-level security; see RedemptionStore's own doc comment).
// invitations/users below are fixture-only helpers built on the same
// privileged pool, so tests can seed state the handler never sees any other
// way to reach (an already-suspended account, an already-expired invitation).
//
// This is what the handler unit tests (fakes, handlers_redeem_test.go) cannot
// prove: that the whole story — atomic consume+provision, single-use under a
// real conditional UPDATE, row-level-security confinement of the membership
// this endpoint writes — holds through the real HTTP surface, not just
// through a fake standing in for it.
type redeemHarness struct {
	router      http.Handler
	priv        *pgxpool.Pool
	appPool     *pgxpool.Pool
	tenants     *postgres.TenantStore
	invitations *postgres.InvitationStore
	users       *postgres.UserStore
}

func realRedeemHarness(t *testing.T) *redeemHarness {
	t.Helper()
	return buildRedeemHarness(t, func(cfg *config.Config) {})
}

// buildRedeemHarness is realRedeemHarness's underlying constructor, taking a
// config mutator so a caller (TestRedeemAPI_RateLimiterIsInTheChain) can turn
// on the same rate limiter production wires without duplicating the pools,
// schema and migration setup above it.
func buildRedeemHarness(t *testing.T, withCfg func(*config.Config)) *redeemHarness {
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
	withCfg(cfg)
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), priv.Ping)

	h := &redeemHarness{
		priv: priv, appPool: appPool,
		tenants:     postgres.NewTenantStore(priv),
		invitations: postgres.NewInvitationStore(priv),
		users:       postgres.NewUserStore(priv),
	}
	s.SetTenants(h.tenants)
	s.SetOrganizations(postgres.NewOrganizationStore(appPool))
	s.SetRedemptions(postgres.NewRedemptionStore(priv))
	h.router = s.Router()
	return h
}

func redeemTestCtx() context.Context { return domain.WithActor(context.Background(), "test-actor") }

// tenantAndOrg registers a real tenant and a real organization naming it,
// both directly via the privileged pool.
func (h *redeemHarness) tenantAndOrg(t *testing.T, slug string) (tenantID, orgID string) {
	t.Helper()
	slug = uniqueSlug(slug)
	tn, err := h.tenants.Create(redeemTestCtx(), &domain.Tenant{
		TenantID: domain.NewID(), Slug: slug, Name: slug, ExternalID: "ext-" + slug,
		Status: domain.TenantActive,
	})
	if err != nil {
		t.Fatalf("create tenant %s: %v", slug, err)
	}
	orgID = domain.NewID()
	if _, err := h.priv.Exec(context.Background(), `
		INSERT INTO organization (org_id, tenant_id, slug, name, status)
		VALUES ($1, $2, $3, $4, 'active')`,
		orgID, tn.TenantID, uniqueSlug("org"), slug+" Inc"); err != nil {
		t.Fatalf("create organization for tenant %s: %v", tn.TenantID, err)
	}
	return tn.TenantID, orgID
}

// issue mints a real, pending invitation directly against the privileged
// pool — the same fixture path postgres's own redemption_store_integration_test.go
// uses — so tests can control expiry/status independently of the admin HTTP
// surface E-ID.4a already covers.
func (h *redeemHarness) issue(t *testing.T, orgID, tenantID, email string, ttl time.Duration) (*domain.Invitation, string) {
	t.Helper()
	inv, token, err := domain.NewInvitation(orgID, tenantID, email, ttl, time.Now())
	if err != nil {
		t.Fatalf("build invitation: %v", err)
	}
	created, err := h.invitations.Create(redeemTestCtx(), inv)
	if err != nil {
		t.Fatalf("create invitation: %v", err)
	}
	return created, token
}

// seedUser inserts an app_user row directly at the given status, bypassing
// the lifecycle's transition rules — exactly what a fixture needs to prove a
// refusal against a pre-existing suspended/deactivated account (the store's
// own Create does not itself validate a transition; only SetStatus does).
func (h *redeemHarness) seedUser(t *testing.T, email string, status domain.UserStatus) *domain.User {
	t.Helper()
	u, err := domain.NewUser(email, "Seeded")
	if err != nil {
		t.Fatalf("build user: %v", err)
	}
	u.Status = status
	created, err := h.users.Create(redeemTestCtx(), u)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return created
}

func (h *redeemHarness) cleanupEmail(t *testing.T, email string) {
	t.Helper()
	t.Cleanup(func() {
		c := context.Background()
		_, _ = h.priv.Exec(c, `DELETE FROM membership WHERE user_id IN
			(SELECT user_id FROM app_user WHERE lower(email::text) = $1)`, domain.NormalizeEmail(email))
		_, _ = h.priv.Exec(c, `DELETE FROM app_user WHERE lower(email::text) = $1`, domain.NormalizeEmail(email))
	})
}

// redeem issues the unauthenticated POST — deliberately with NO Authorization
// header, which is the entire point of this endpoint.
func (h *redeemHarness) redeem(token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/auth/invitations/redeem",
		strings.NewReader(`{"token":"`+token+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h *redeemHarness) countMemberships(t *testing.T, orgID string) int {
	t.Helper()
	var n int
	if err := h.priv.QueryRow(context.Background(),
		`SELECT count(*) FROM membership WHERE org_id = $1 AND status = 'active'`, orgID).Scan(&n); err != nil {
		t.Fatalf("count memberships: %v", err)
	}
	return n
}

func (h *redeemHarness) invitationStatus(t *testing.T, invitationID string) domain.InvitationStatus {
	t.Helper()
	got, err := h.invitations.Get(redeemTestCtx(), invitationID)
	if err != nil {
		t.Fatalf("get invitation: %v", err)
	}
	return got.Status
}

func (h *redeemHarness) userStatus(t *testing.T, email string) (domain.UserStatus, bool) {
	t.Helper()
	u, err := h.users.GetByEmail(redeemTestCtx(), email)
	if err != nil {
		if _, ok := domain.AsValidation(err); ok {
			t.Fatalf("get user by email: %v", err)
		}
		return "", false
	}
	return u.Status, true
}

// auditActsFor returns the operations recorded on the admin audit chain for
// orgID, so a test can prove WHO the redemption was attributed to without
// depending on the read-side query surface (which E-ID.4a's admin endpoints
// already exercise elsewhere).
func (h *redeemHarness) auditActsFor(t *testing.T, orgID string) []struct {
	Operation string
	Actor     string
} {
	t.Helper()
	rows, err := h.priv.Query(context.Background(),
		`SELECT operation, actor FROM admin_audit_event WHERE subject_org_id = $1 ORDER BY seq`, orgID)
	if err != nil {
		t.Fatalf("query admin audit: %v", err)
	}
	defer rows.Close()
	var out []struct {
		Operation string
		Actor     string
	}
	for rows.Next() {
		var rec struct {
			Operation string
			Actor     string
		}
		if err := rows.Scan(&rec.Operation, &rec.Actor); err != nil {
			t.Fatalf("scan admin audit row: %v", err)
		}
		out = append(out, rec)
	}
	return out
}

// ---- happy path ---------------------------------------------------------------

// TestRedeemAPI_HappyPath proves the whole story end to end, over the real
// HTTP surface with no Authorization header: the invited email becomes an
// active user and an active member of the invitation's organization, the
// invitation itself is redeemed, and the audit trail attributes every act to
// domain.InvitationBearerActor.
func TestRedeemAPI_HappyPath(t *testing.T) {
	h := realRedeemHarness(t)
	tenantID, orgID := h.tenantAndOrg(t, "redeem-happy")
	const email = "new-member@example.com"
	h.cleanupEmail(t, email)

	inv, token := h.issue(t, orgID, tenantID, email, 48*time.Hour)

	rec := h.redeem(token)
	if rec.Code != http.StatusOK {
		t.Fatalf("redeem: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var resp redeemInvitationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body %s)", err, rec.Body.String())
	}
	if resp.Organization == "" {
		t.Error("response carries no organization name")
	}

	status, ok := h.userStatus(t, email)
	if !ok {
		t.Fatal("no user was provisioned for the invited email")
	}
	if status != domain.UserActive {
		t.Errorf("user status = %q, want active", status)
	}
	if n := h.countMemberships(t, orgID); n != 1 {
		t.Errorf("%d active memberships in the invitation's org, want 1", n)
	}
	if got := h.invitationStatus(t, inv.InvitationID); got != domain.InvitationRedeemed {
		t.Errorf("invitation status = %q, want redeemed", got)
	}

	// invitation.created was written by this test's own fixture (h.issue,
	// attributed to "test-actor"); everything the redemption itself wrote
	// must be attributed to domain.InvitationBearerActor, never to a session
	// that never existed.
	redeemOps := map[string]bool{"invitation.redeemed": true, "user.created": true, "membership.granted": true}
	acts := h.auditActsFor(t, orgID)
	if len(acts) == 0 {
		t.Fatal("no administrative audit acts were recorded for this organization")
	}
	sawRedeemed := false
	for _, a := range acts {
		if !redeemOps[a.Operation] {
			continue
		}
		if a.Actor != domain.InvitationBearerActor {
			t.Errorf("audit act %q attributed to actor %q, want %q", a.Operation, a.Actor, domain.InvitationBearerActor)
		}
		if a.Operation == "invitation.redeemed" {
			sawRedeemed = true
		}
	}
	if !sawRedeemed {
		t.Errorf("audit trail %+v does not include invitation.redeemed", acts)
	}
}

// ---- double redeem / replay ----------------------------------------------------

// TestRedeemAPI_DoubleRedeemIsRefusedAndDoesNotDuplicate proves single-use at
// the HTTP layer: the second POST with the same token gets the generic
// refusal, and neither the membership nor the user is affected by the replay.
func TestRedeemAPI_DoubleRedeemIsRefusedAndDoesNotDuplicate(t *testing.T) {
	h := realRedeemHarness(t)
	tenantID, orgID := h.tenantAndOrg(t, "redeem-replay")
	const email = "replay-member@example.com"
	h.cleanupEmail(t, email)

	_, token := h.issue(t, orgID, tenantID, email, 48*time.Hour)

	first := h.redeem(token)
	if first.Code != http.StatusOK {
		t.Fatalf("first redeem: status = %d, want 200: %s", first.Code, first.Body.String())
	}

	second := h.redeem(token)
	if second.Code != http.StatusBadRequest {
		t.Fatalf("second redeem: status = %d, want 400 (generic refusal): %s", second.Code, second.Body.String())
	}

	if n := h.countMemberships(t, orgID); n != 1 {
		t.Errorf("%d active memberships after a replay, want 1 (no duplicate)", n)
	}
	status, ok := h.userStatus(t, email)
	if !ok || status != domain.UserActive {
		t.Errorf("user after replay: status=%v ok=%v, want active/true unchanged", status, ok)
	}
}

// ---- expired / revoked / unknown: identical generic failure --------------------

// TestRedeemAPI_ExpiredRevokedAndUnknownAreIndistinguishable is the
// enumeration-resistance property: three different underlying causes must
// produce byte-identical response shape (status, title, detail — everything
// except the per-request instance id).
func TestRedeemAPI_ExpiredRevokedAndUnknownAreIndistinguishable(t *testing.T) {
	h := realRedeemHarness(t)
	tenantID, orgID := h.tenantAndOrg(t, "redeem-indistinct")

	// Expired.
	emailExp := "expired-member@example.com"
	h.cleanupEmail(t, emailExp)
	invExp, tokenExp := h.issue(t, orgID, tenantID, emailExp, time.Hour)
	if _, err := h.priv.Exec(context.Background(),
		`UPDATE invitation SET expires_at = now() - interval '1 second' WHERE invitation_id = $1`,
		invExp.InvitationID); err != nil {
		t.Fatalf("force expiry: %v", err)
	}

	// Revoked.
	emailRev := "revoked-member@example.com"
	h.cleanupEmail(t, emailRev)
	invRev, tokenRev := h.issue(t, orgID, tenantID, emailRev, 48*time.Hour)
	if _, err := h.invitations.Revoke(redeemTestCtx(), invRev.InvitationID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// Unknown: a well-formed token that was never issued.
	_, neverIssued, err := domain.NewInvitation(orgID, tenantID, "unknown-member@example.com", 48*time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	responses := map[string]*httptest.ResponseRecorder{
		"expired": h.redeem(tokenExp),
		"revoked": h.redeem(tokenRev),
		"unknown": h.redeem(neverIssued),
	}

	var reference string
	for name, rec := range responses {
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", name, rec.Code, rec.Body.String())
		}
		norm := normalizeInstance(t, rec.Body.String())
		if reference == "" {
			reference = norm
			continue
		}
		if norm != reference {
			t.Errorf("%s response shape differs from the reference:\n  reference: %s\n  got:       %s",
				name, reference, norm)
		}
	}
}

// ---- suspended / deactivated: abort the whole redemption -----------------------

// TestRedeemAPI_SuspendedAndDeactivatedAccountsAbortTheRedemption proves the
// refusal has teeth at the HTTP layer: the invitation stays pending, no
// membership is granted, and the account is not reactivated — matching the
// generic 400 every other cause produces.
func TestRedeemAPI_SuspendedAndDeactivatedAccountsAbortTheRedemption(t *testing.T) {
	for _, st := range []domain.UserStatus{domain.UserSuspended, domain.UserDeactivated} {
		t.Run(string(st), func(t *testing.T) {
			h := realRedeemHarness(t)
			tenantID, orgID := h.tenantAndOrg(t, "redeem-blocked-"+string(st))
			email := string(st) + "-blocked@example.com"
			h.cleanupEmail(t, email)
			h.seedUser(t, email, st)

			inv, token := h.issue(t, orgID, tenantID, email, 48*time.Hour)

			rec := h.redeem(token)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("redeem against a %s account: status = %d, want 400: %s", st, rec.Code, rec.Body.String())
			}

			if got := h.invitationStatus(t, inv.InvitationID); got != domain.InvitationPending {
				t.Errorf("invitation status = %q after a refused redemption, want still pending", got)
			}
			if n := h.countMemberships(t, orgID); n != 0 {
				t.Errorf("%d memberships were granted to a %s account, want 0", n, st)
			}
			status, ok := h.userStatus(t, email)
			if !ok || status != st {
				t.Errorf("account status after refused redemption: status=%v ok=%v, want %q/true unchanged", status, ok, st)
			}
		})
	}
}

// ---- cross-tenant confinement ---------------------------------------------------

// TestRedeemAPI_MembershipLandsOnlyInTheInvitationsTenant proves there is no
// request field that can steer which tenant a redemption provisions into: the
// request body carries only the token, and the membership this endpoint
// writes is visible under the invitation's own tenant and invisible under a
// second, unrelated one — the row-level-security confinement the privileged
// write must still respect by construction (it labels the row with the
// invitation's tenant_id, never anything from the request).
func TestRedeemAPI_MembershipLandsOnlyInTheInvitationsTenant(t *testing.T) {
	h := realRedeemHarness(t)
	tenantA, orgA := h.tenantAndOrg(t, "redeem-tenant-a")
	tenantB, _ := h.tenantAndOrg(t, "redeem-tenant-b")
	const email = "cross-tenant-member@example.com"
	h.cleanupEmail(t, email)

	_, token := h.issue(t, orgA, tenantA, email, 48*time.Hour)
	rec := h.redeem(token)
	if rec.Code != http.StatusOK {
		t.Fatalf("redeem: status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	var membershipID string
	if err := h.priv.QueryRow(context.Background(),
		`SELECT membership_id FROM membership WHERE org_id = $1`, orgA).Scan(&membershipID); err != nil {
		t.Fatalf("read back membership: %v", err)
	}

	count := func(tenantID string) int {
		t.Helper()
		var n int
		ctx := domain.WithTenant(context.Background(), &domain.Tenant{TenantID: tenantID})
		if err := h.appPool.QueryRow(ctx,
			`SELECT count(*) FROM membership WHERE membership_id = $1`, membershipID).Scan(&n); err != nil {
			t.Fatalf("scoped read as %s: %v", tenantID, err)
		}
		return n
	}
	if n := count(tenantA); n != 1 {
		t.Errorf("tenant A sees %d of its own memberships, want 1", n)
	}
	if n := count(tenantB); n != 0 {
		t.Errorf("tenant B sees %d of tenant A's memberships, want 0 — cross-tenant disclosure", n)
	}
}

// ---- idempotent membership -------------------------------------------------------

// TestRedeemAPI_SecondInvitationForAnAlreadyActiveMemberIsIdempotent covers the
// edge the store's own doc comment calls out: re-inviting someone who is
// already an active member must behave sanely (succeed, not duplicate) rather
// than erroring.
func TestRedeemAPI_SecondInvitationForAnAlreadyActiveMemberIsIdempotent(t *testing.T) {
	h := realRedeemHarness(t)
	tenantID, orgID := h.tenantAndOrg(t, "redeem-idempotent")
	const email = "already-member@example.com"
	h.cleanupEmail(t, email)

	_, firstToken := h.issue(t, orgID, tenantID, email, 48*time.Hour)
	first := h.redeem(firstToken)
	if first.Code != http.StatusOK {
		t.Fatalf("first redeem: status = %d, want 200: %s", first.Code, first.Body.String())
	}

	_, secondToken := h.issue(t, orgID, tenantID, email, 48*time.Hour)
	second := h.redeem(secondToken)
	if second.Code != http.StatusOK {
		t.Fatalf("second redeem (already an active member): status = %d, want 200: %s",
			second.Code, second.Body.String())
	}

	if n := h.countMemberships(t, orgID); n != 1 {
		t.Errorf("%d active memberships after redeeming a second invitation for the same member, want 1", n)
	}
}

// ---- rate limiting is wired ------------------------------------------------------

// TestRedeemAPI_RateLimiterIsInTheChain proves the middleware is actually
// wired into this route, not the token-bucket algorithm itself (that is
// ratelimit_middleware_test.go's job). With a burst of exactly 1 configured
// the same way production does (cfg.RateLimitEnabled/RPS/Burst, read by
// NewServer), the first unauthenticated redeem attempt against this instance
// consumes the bucket and the second — a different token, proving this is
// about the ROUTE and not about one token's own state — is refused with 429
// before the store is ever reached.
func TestRedeemAPI_RateLimiterIsInTheChain(t *testing.T) {
	h := buildRedeemHarness(t, func(cfg *config.Config) {
		cfg.RateLimitEnabled = true
		cfg.RateLimitRPS = 1
		cfg.RateLimitBurst = 1
	})
	tenantID, orgID := h.tenantAndOrg(t, "redeem-ratelimited")

	emailA, emailB := "throttled-a@example.com", "throttled-b@example.com"
	h.cleanupEmail(t, emailA)
	h.cleanupEmail(t, emailB)
	_, tokenA := h.issue(t, orgID, tenantID, emailA, 48*time.Hour)
	_, tokenB := h.issue(t, orgID, tenantID, emailB, 48*time.Hour)

	first := h.redeem(tokenA)
	if first.Code == http.StatusTooManyRequests {
		t.Fatalf("the very first request against a fresh instance was already rate-limited: %s", first.Body.String())
	}

	second := h.redeem(tokenB)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second immediate request: status = %d, want 429 (burst of 1 exhausted): %s",
			second.Code, second.Body.String())
	}
	if second.Header().Get("Retry-After") == "" {
		t.Error("429 response carries no Retry-After header")
	}

	// The second, throttled attempt must not have reached the store: its
	// token was never redeemed.
	status, ok := h.userStatus(t, emailB)
	if ok {
		t.Errorf("a rate-limited request still provisioned a user (status=%v)", status)
	}
}
