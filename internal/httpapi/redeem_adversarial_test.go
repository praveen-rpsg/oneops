//go:build integration

package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/auth"
	"github.com/rpsg/oneops/internal/config"
	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/observability"
)

// postJSON posts an arbitrary raw body to the redeem endpoint (h.redeem only
// allows a well-formed {"token":...}). Used to smuggle extra fields and to send
// oversized/malformed payloads the normal helper cannot express.
func (h *redeemHarness) postJSON(raw string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/auth/invitations/redeem", strings.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// ATTACK 3 (race): two simultaneous redeems of the SAME token. Exactly one must
// win (200), the other must get the generic 400, and there must be exactly one
// active membership. Proves the conditional UPDATE serializes under real
// concurrency, not just sequentially.
func TestADV_ConcurrentDoubleRedeem_ExactlyOneWins(t *testing.T) {
	h := realRedeemHarness(t)
	tenantID, orgID := h.tenantAndOrg(t, "adv-race")
	const email = "race-member@example.com"
	h.cleanupEmail(t, email)
	_, token := h.issue(t, orgID, tenantID, email, 48*time.Hour)

	const n = 8
	var wg sync.WaitGroup
	codes := make([]int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			codes[i] = h.redeem(token).Code
		}(i)
	}
	close(start)
	wg.Wait()

	ok, bad, other := 0, 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusOK:
			ok++
		case http.StatusBadRequest:
			bad++
		default:
			other++
		}
	}
	if ok != 1 {
		t.Errorf("concurrent redeem: %d winners, want exactly 1 (codes=%v)", ok, codes)
	}
	if other != 0 {
		t.Errorf("concurrent redeem produced %d non-200/400 responses (codes=%v)", other, codes)
	}
	if n := h.countMemberships(t, orgID); n != 1 {
		t.Errorf("%d active memberships after a concurrent race, want exactly 1", n)
	}
}

// ATTACK 2 (cross-tenant steering): the body carries the token PLUS attacker-
// chosen org_id/tenant_id/email/user_id. They must be silently ignored; the
// membership must land in the invitation's own tenant/org and name the invited
// email, never anything from the request.
func TestADV_SmuggledBodyFieldsAreIgnored(t *testing.T) {
	h := realRedeemHarness(t)
	tenantVictim, orgVictim := h.tenantAndOrg(t, "adv-victim")
	tenantAttacker, orgAttacker := h.tenantAndOrg(t, "adv-attacker")
	const invited = "smuggle-invited@example.com"
	h.cleanupEmail(t, invited)
	h.cleanupEmail(t, "attacker-chosen@evil.com")

	_, token := h.issue(t, orgVictim, tenantVictim, invited, 48*time.Hour)

	raw := fmt.Sprintf(`{"token":%q,"org_id":%q,"tenant_id":%q,"email":%q,"user_id":%q,"status":"admin","display_name":"pwned"}`,
		token, orgAttacker, tenantAttacker, "attacker-chosen@evil.com", domain.NewID())
	rec := h.postJSON(raw)
	if rec.Code != http.StatusOK {
		t.Fatalf("redeem with smuggled fields: status=%d, want 200: %s", rec.Code, rec.Body.String())
	}

	// Membership lands in the invitation's org, not the attacker's.
	if n := h.countMemberships(t, orgVictim); n != 1 {
		t.Errorf("invitation org has %d memberships, want 1", n)
	}
	if n := h.countMemberships(t, orgAttacker); n != 0 {
		t.Errorf("attacker-named org has %d memberships, want 0 — body steered provisioning", n)
	}
	// The account created is the invited email, not the smuggled one.
	if _, ok := h.userStatus(t, invited); !ok {
		t.Error("invited email was not provisioned")
	}
	if _, ok := h.userStatus(t, "attacker-chosen@evil.com"); ok {
		t.Error("attacker-chosen email was provisioned — body email was trusted")
	}
	// The membership's tenant column is the invitation's tenant.
	var gotTenant string
	if err := h.priv.QueryRow(context.Background(),
		`SELECT tenant_id FROM membership WHERE org_id=$1`, orgVictim).Scan(&gotTenant); err != nil {
		t.Fatalf("read membership tenant: %v", err)
	}
	if gotTenant != tenantVictim {
		t.Errorf("membership tenant_id=%s, want the invitation's %s", gotTenant, tenantVictim)
	}
}

// ATTACK 5 (email confusables / case / whitespace): an invitation whose email
// differs only by case/surrounding space from an EXISTING active account must
// attach to that same account (identity is case-folded+trimmed), never mint a
// duplicate. Homoglyph/unicode addresses are DIFFERENT byte sequences and must
// NOT collide with an ASCII account.
func TestADV_EmailCaseAndWhitespaceFoldToSameAccount(t *testing.T) {
	h := realRedeemHarness(t)
	tenantID, orgID := h.tenantAndOrg(t, "adv-email")
	const canonical = "casefold-user@example.com"
	h.cleanupEmail(t, canonical)

	existing := h.seedUser(t, canonical, domain.UserActive)

	// Invitation for a case/space variant of the same address.
	_, token := h.issue(t, orgID, tenantID, "  CaseFold-User@Example.COM  ", 48*time.Hour)
	rec := h.redeem(token)
	if rec.Code != http.StatusOK {
		t.Fatalf("redeem case-variant: status=%d, want 200: %s", rec.Code, rec.Body.String())
	}

	// No duplicate account: still exactly one row for this address.
	var userRows int
	if err := h.priv.QueryRow(context.Background(),
		`SELECT count(*) FROM app_user WHERE lower(email::text)=$1`, domain.NormalizeEmail(canonical)).Scan(&userRows); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if userRows != 1 {
		t.Errorf("%d accounts for the case-variant address, want 1 (no duplicate)", userRows)
	}
	// Membership attaches to the pre-existing account.
	var memberUser string
	if err := h.priv.QueryRow(context.Background(),
		`SELECT user_id FROM membership WHERE org_id=$1`, orgID).Scan(&memberUser); err != nil {
		t.Fatalf("read membership user: %v", err)
	}
	if memberUser != existing.UserID {
		t.Errorf("membership names user %s, want the pre-existing %s", memberUser, existing.UserID)
	}
}

// ATTACK 8 (oversized body — no size cap flagged): a multi-megabyte token value.
// This documents whether the handler bounds the request body. If it reads the
// whole thing (returns the generic 400 after hashing, not 413), there is no
// MaxBytesReader in the chain: unauthenticated, pre-provision memory allocation
// scales with attacker input.
func TestADV_OversizedBody_NoSizeCap(t *testing.T) {
	h := realRedeemHarness(t)

	const megs = 8
	big := strings.Repeat("A", megs*1024*1024)
	rec := h.postJSON(`{"token":"` + big + `"}`)

	// GUARD (E-ID.4b security review, finding #1): the handler caps the body via
	// http.MaxBytesReader (4 KiB), so an oversized body is refused with 413
	// BEFORE it is fully read/hashed. Removing the cap makes this bite (the
	// handler would return the generic 400 after allocating the whole body).
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized (%d MiB) body: status=%d, want 413 — the request-body size cap on this unauthenticated endpoint is missing or removed", megs, rec.Code)
	}
}

// ATTACK 8 (malformed / empty / null-byte tokens): none may panic or 500; each
// is the generic 400 (or invalid-body 400 for non-JSON).
func TestADV_MalformedTokensDoNotCrash(t *testing.T) {
	h := realRedeemHarness(t)

	cases := map[string]string{
		"empty token":      `{"token":""}`,
		"whitespace token": `{"token":"    "}`,
		"null byte token":  `{"token":"ab\u0000cd"}`,
		"unicode token":    `{"token":"héllo-世界-🔑"}`,
		"missing token":    `{}`,
		"wrong type token": `{"token":12345}`,
		"not json":         `not json at all`,
		"array body":       `["token"]`,
		"trailing garbage": `{"token":"x"}garbage`,
	}
	for name, body := range cases {
		rec := h.postJSON(body)
		if rec.Code >= 500 {
			t.Errorf("%s: status=%d (server fault) — want a 4xx client error: %s", name, rec.Code, rec.Body.String())
		}
	}
}

// ATTACK 4 (invariantGate protects the privileged write): when the platform
// invariant is breached, the redeem route must refuse with 503 BEFORE the
// RedemptionStore (privileged pool) is ever touched. Uses a fake so a call to
// the store is observable; proves the gate is wired on THIS route, not just in
// isolation.
func TestADV_InvariantBreach_RefusesBeforeStoreIsTouched(t *testing.T) {
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	cfg := &config.Config{
		HTTPAddr: ":0", DefaultPageSize: 50, MaxPageSize: 200,
		AuthEnabled: true, JWTIssuer: tIss, JWTAudience: tAud, JWTHMACKey: tSecret,
	}
	s := NewServer(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)),
		newFakeRepo(), newFakeIdem(), auth.NewVerifier(tIss, tAud, tSecret, ""),
		observability.NewMetrics(), func(context.Context) error { return nil })
	fake := newFakeRedemptions()
	s.SetRedemptions(fake)
	s.SetInvariantGate(func() error { return errors.New("row-level security is off") })
	router := s.Router()

	req := httptest.NewRequest(http.MethodPost, "/auth/invitations/redeem",
		strings.NewReader(`{"token":"anything"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("breached invariant: status=%d, want 503: %s", rec.Code, rec.Body.String())
	}
	if fake.calls != 0 {
		t.Errorf("the redemption store was reached %d time(s) while the invariant was breached — the privileged write is not gated", fake.calls)
	}
	if strings.Contains(rec.Body.String(), "row-level security") {
		t.Errorf("503 body discloses the specific broken invariant to an unauthenticated caller: %s", rec.Body.String())
	}
}
