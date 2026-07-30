//go:build integration

package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

// redemptionFixture seeds a tenant + organisation and returns the two stores
// redemption needs, both on the privileged pool.
func redemptionFixture(t *testing.T, suffix string) (*pgxpool.Pool, *InvitationStore, *RedemptionStore, string, string) {
	t.Helper()
	priv := testPool(t)
	ctx := adminTestCtx()
	tenantID, orgID := seedOrgForInvitations(ctx, t, priv, suffix)
	t.Cleanup(func() {
		c := context.Background()
		_, _ = priv.Exec(c, `DELETE FROM membership WHERE org_id = $1`, orgID)
	})
	return priv, NewInvitationStore(priv), NewRedemptionStore(priv), tenantID, orgID
}

// cleanupUser removes an account seeded or created by a test. app_user is
// global, so a leftover row collides with the next run on the unique address.
func cleanupUser(t *testing.T, priv *pgxpool.Pool, email string) {
	t.Helper()
	t.Cleanup(func() {
		c := context.Background()
		_, _ = priv.Exec(c, `DELETE FROM membership WHERE user_id IN
			(SELECT user_id FROM app_user WHERE lower(email::text) = $1)`, domain.NormalizeEmail(email))
		_, _ = priv.Exec(c, `DELETE FROM app_user WHERE lower(email::text) = $1`, domain.NormalizeEmail(email))
	})
}

// The happy path for someone with no account: redemption creates the user and
// the membership, and consumes the invitation, all at once.
func TestRedemption_NewUserGetsAccountAndMembership(t *testing.T) {
	priv, invites, redeem, tenantID, orgID := redemptionFixture(t, "new-user")
	ctx := adminTestCtx()
	const email = "brand-new@example.com"
	cleanupUser(t, priv, email)

	_, token := issue(t, ctx, invites, orgID, tenantID, email, invTTL)

	got, err := redeem.Redeem(ctx, token, "  Brand New  ")
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	if !got.UserCreated {
		t.Error("UserCreated is false, but no account existed for this address")
	}
	if !got.MembershipCreated {
		t.Error("MembershipCreated is false, but no membership existed")
	}
	if got.User.Email != email {
		t.Errorf("user email %q, want %q", got.User.Email, email)
	}
	if got.User.DisplayName != "Brand New" {
		t.Errorf("display name %q, want the aggregate's trimmed form", got.User.DisplayName)
	}
	// The account exists because someone accepted; that is exactly the evidence
	// `invited` waits for.
	if got.User.Status != domain.UserActive {
		t.Errorf("new user status %q, want active", got.User.Status)
	}
	if got.Membership.OrgID != orgID || got.Membership.UserID != got.User.UserID {
		t.Errorf("membership %+v does not join the invited user to the organisation", got.Membership)
	}
	// The isolation key must come from the invitation, never be defaulted.
	if got.Membership.TenantID != tenantID {
		t.Errorf("membership tenant_id %q, want %q from the invitation — a membership labelled "+
			"with the wrong tenant grants access to the wrong boundary",
			got.Membership.TenantID, tenantID)
	}
	if got.Membership.Status != domain.MembershipActive {
		t.Errorf("membership status %q, want active", got.Membership.Status)
	}
	if got.Invitation.Status != domain.InvitationRedeemed {
		t.Errorf("invitation status %q, want redeemed", got.Invitation.Status)
	}

	// All three writes are visible together.
	var users, memberships, redeemed int
	if err := priv.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM app_user WHERE lower(email::text) = $1),
		       (SELECT count(*) FROM membership WHERE org_id = $2 AND status = 'active'),
		       (SELECT count(*) FROM invitation WHERE org_id = $2 AND status = 'redeemed')`,
		email, orgID).Scan(&users, &memberships, &redeemed); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if users != 1 || memberships != 1 || redeemed != 1 {
		t.Errorf("after redemption: users=%d memberships=%d redeemed=%d, want 1/1/1",
			users, memberships, redeemed)
	}
}

// An address that already has an account must attach to it, not mint a second.
// app_user is unique on the address platform-wide (ADR-IDENTITY-001 §8.2).
func TestRedemption_ExistingUserIsReusedNotDuplicated(t *testing.T) {
	priv, invites, redeem, tenantID, orgID := redemptionFixture(t, "existing-user")
	ctx := adminTestCtx()
	const email = "already-here@example.com"
	cleanupUser(t, priv, email)

	users := NewUserStore(priv)
	existing, err := domain.NewUser(email, "Already Here")
	if err != nil {
		t.Fatal(err)
	}
	existing.Status = domain.UserActive
	created, err := users.Create(ctx, existing)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}

	_, token := issue(t, ctx, invites, orgID, tenantID, email, invTTL)

	got, err := redeem.Redeem(ctx, token, "Someone Elses Name")
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if got.UserCreated {
		t.Error("UserCreated is true, but the address already had an account")
	}
	if got.User.UserID != created.UserID {
		t.Errorf("redemption used user %s, want the existing %s", got.User.UserID, created.UserID)
	}
	// An invitation is not a licence to rename someone else's account.
	if got.User.DisplayName != "Already Here" {
		t.Errorf("display name became %q; an existing account must not be renamed by whoever "+
			"issued the invitation", got.User.DisplayName)
	}

	var count int
	if err := priv.QueryRow(ctx, `SELECT count(*) FROM app_user WHERE lower(email::text) = $1`,
		email).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("%d accounts exist for %s, want 1", count, email)
	}
}

// A user still in `invited` is activated by accepting, which is the transition
// the lifecycle already defines.
func TestRedemption_InvitedUserIsActivated(t *testing.T) {
	priv, invites, redeem, tenantID, orgID := redemptionFixture(t, "activate")
	ctx := adminTestCtx()
	const email = "still-invited@example.com"
	cleanupUser(t, priv, email)

	users := NewUserStore(priv)
	pending, err := domain.NewUser(email, "Still Invited")
	if err != nil {
		t.Fatal(err)
	}
	seeded, err := users.Create(ctx, pending)
	if err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if seeded.Status != domain.UserInvited {
		t.Fatalf("precondition: seeded user is %q, want invited", seeded.Status)
	}

	_, token := issue(t, ctx, invites, orgID, tenantID, email, invTTL)

	got, err := redeem.Redeem(ctx, token, "")
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if got.User.Status != domain.UserActive {
		t.Errorf("status after redemption is %q, want active", got.User.Status)
	}
	if got.User.RowVersion <= seeded.RowVersion {
		t.Errorf("row_version %d did not advance past %d; the activation was not written",
			got.User.RowVersion, seeded.RowVersion)
	}
}

// Suspension must not be overturnable by an invitation link, and deactivation
// is terminal. Both refuse, both roll back, and both are indistinguishable from
// an unknown token.
func TestRedemption_RefusesSuspendedAndDeactivatedAccounts(t *testing.T) {
	for _, st := range []domain.UserStatus{domain.UserSuspended, domain.UserDeactivated} {
		t.Run(string(st), func(t *testing.T) {
			priv, invites, redeem, tenantID, orgID := redemptionFixture(t, "refuse-"+string(st))
			ctx := adminTestCtx()
			email := string(st) + "-user@example.com"
			cleanupUser(t, priv, email)

			users := NewUserStore(priv)
			u, err := domain.NewUser(email, "Blocked")
			if err != nil {
				t.Fatal(err)
			}
			u.Status = st
			if _, err := users.Create(ctx, u); err != nil {
				t.Fatalf("seed user: %v", err)
			}

			inv, token := issue(t, ctx, invites, orgID, tenantID, email, invTTL)

			if _, err := redeem.Redeem(ctx, token, ""); !errors.Is(err, domain.ErrTokenNotRedeemable) {
				t.Fatalf("got %v, want ErrTokenNotRedeemable — the refusal must not reveal that "+
					"the address belongs to a %s account", err, st)
			}

			// The whole transaction rolled back: no membership, and the token is
			// NOT burned.
			var memberships int
			if err := priv.QueryRow(ctx,
				`SELECT count(*) FROM membership WHERE org_id = $1`, orgID).Scan(&memberships); err != nil {
				t.Fatal(err)
			}
			if memberships != 0 {
				t.Errorf("%d memberships were granted to a %s account", memberships, st)
			}
			after, err := invites.Get(ctx, inv.InvitationID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != domain.InvitationPending {
				t.Errorf("invitation is %q after a refused redemption, want pending — a refused "+
					"redemption must consume nothing", after.Status)
			}
		})
	}
}

// Replay: the second redemption of a token must fail, and must fail the same
// way an unknown token does.
func TestRedemption_ReplayIsRefused(t *testing.T) {
	priv, invites, redeem, tenantID, orgID := redemptionFixture(t, "replay")
	ctx := adminTestCtx()
	const email = "replay@example.com"
	cleanupUser(t, priv, email)

	_, token := issue(t, ctx, invites, orgID, tenantID, email, invTTL)

	if _, err := redeem.Redeem(ctx, token, "Replay"); err != nil {
		t.Fatalf("first Redeem: %v", err)
	}
	if _, err := redeem.Redeem(ctx, token, "Replay"); !errors.Is(err, domain.ErrTokenNotRedeemable) {
		t.Fatalf("replay: got %v, want ErrTokenNotRedeemable", err)
	}

	// The replay created nothing.
	var memberships, users int
	if err := priv.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM membership WHERE org_id = $1),
		       (SELECT count(*) FROM app_user WHERE lower(email::text) = $2)`,
		orgID, email).Scan(&memberships, &users); err != nil {
		t.Fatal(err)
	}
	if memberships != 1 || users != 1 {
		t.Errorf("after a replay: memberships=%d users=%d, want 1/1", memberships, users)
	}
}

// Concurrent redemption of one token must produce exactly one winner, one
// account and one membership.
//
// This is what a sequential replay test cannot prove. A read-then-write consume
// passes the replay test and fails here.
func TestRedemption_ConcurrentRedemptionHasExactlyOneWinner(t *testing.T) {
	priv, invites, redeem, tenantID, orgID := redemptionFixture(t, "race")
	ctx := adminTestCtx()
	const email = "race@example.com"
	cleanupUser(t, priv, email)

	const racers = 8
	_, token := issue(t, ctx, invites, orgID, tenantID, email, invTTL)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners int
		others  []error
	)
	start := make(chan struct{})
	for n := 0; n < racers; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := redeem.Redeem(ctx, token, "Racer")
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				winners++
			case errors.Is(err, domain.ErrTokenNotRedeemable):
			default:
				others = append(others, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if winners != 1 {
		t.Errorf("%d of %d concurrent redemptions succeeded, want exactly 1", winners, racers)
	}
	if len(others) != 0 {
		t.Errorf("unexpected errors from losing racers: %v", others)
	}

	var memberships, users int
	if err := priv.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM membership WHERE org_id = $1),
		       (SELECT count(*) FROM app_user WHERE lower(email::text) = $2)`,
		orgID, email).Scan(&memberships, &users); err != nil {
		t.Fatal(err)
	}
	if memberships != 1 {
		t.Errorf("%d memberships after a concurrent race, want 1", memberships)
	}
	if users != 1 {
		t.Errorf("%d accounts after a concurrent race, want 1", users)
	}
}

// Two different invitations to the same organisation for the same person must
// not produce two memberships. uq_membership_org_user makes it impossible; this
// proves the store handles the conflict rather than failing.
func TestRedemption_SecondInvitationDoesNotDuplicateMembership(t *testing.T) {
	priv, invites, redeem, tenantID, orgID := redemptionFixture(t, "dup-membership")
	ctx := adminTestCtx()
	const email = "twice@example.com"
	cleanupUser(t, priv, email)

	_, firstToken := issue(t, ctx, invites, orgID, tenantID, email, invTTL)
	_, secondToken := issue(t, ctx, invites, orgID, tenantID, email, invTTL)

	first, err := redeem.Redeem(ctx, firstToken, "Twice")
	if err != nil {
		t.Fatalf("first Redeem: %v", err)
	}
	if !first.MembershipCreated {
		t.Error("the first redemption did not create a membership")
	}

	second, err := redeem.Redeem(ctx, secondToken, "Twice")
	if err != nil {
		t.Fatalf("second Redeem: %v", err)
	}
	if second.MembershipCreated {
		t.Error("MembershipCreated is true on the second redemption; the user was already a member")
	}
	if second.Membership.MembershipID != first.Membership.MembershipID {
		t.Errorf("second redemption produced membership %s, want the existing %s",
			second.Membership.MembershipID, first.Membership.MembershipID)
	}
	// An already-active membership must not have its row version consumed: a
	// concurrent administrator may be holding it.
	if second.Membership.RowVersion != first.Membership.RowVersion {
		t.Errorf("row_version moved from %d to %d on a redemption that changed nothing",
			first.Membership.RowVersion, second.Membership.RowVersion)
	}

	var count int
	if err := priv.QueryRow(ctx,
		`SELECT count(*) FROM membership WHERE org_id = $1`, orgID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("%d memberships exist, want 1", count)
	}
}

// Re-inviting someone who was removed must restore their access. That is what
// the conflict update is for.
func TestRedemption_RevokedMembershipIsReactivated(t *testing.T) {
	priv, invites, redeem, tenantID, orgID := redemptionFixture(t, "reactivate")
	ctx := adminTestCtx()
	const email = "returning@example.com"
	cleanupUser(t, priv, email)

	_, firstToken := issue(t, ctx, invites, orgID, tenantID, email, invTTL)
	first, err := redeem.Redeem(ctx, firstToken, "Returning")
	if err != nil {
		t.Fatalf("first Redeem: %v", err)
	}

	if _, err := priv.Exec(ctx,
		`UPDATE membership SET status = 'revoked' WHERE membership_id = $1`,
		first.Membership.MembershipID); err != nil {
		t.Fatalf("revoke membership: %v", err)
	}

	_, secondToken := issue(t, ctx, invites, orgID, tenantID, email, invTTL)
	second, err := redeem.Redeem(ctx, secondToken, "Returning")
	if err != nil {
		t.Fatalf("second Redeem: %v", err)
	}
	if second.Membership.Status != domain.MembershipActive {
		t.Errorf("membership status %q after re-invitation, want active", second.Membership.Status)
	}
	if second.Membership.MembershipID != first.Membership.MembershipID {
		t.Error("re-invitation created a second membership row rather than restoring the first")
	}
	if second.Membership.RowVersion <= first.Membership.RowVersion {
		t.Errorf("row_version %d did not advance past %d; the reactivation was not written",
			second.Membership.RowVersion, first.Membership.RowVersion)
	}
}

// Expired and revoked invitations are refused, and consume nothing.
func TestRedemption_RefusesExpiredAndRevokedInvitations(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		priv, invites, redeem, tenantID, orgID := redemptionFixture(t, "exp")
		ctx := adminTestCtx()
		const email = "expired-redeem@example.com"
		cleanupUser(t, priv, email)

		inv, token := issue(t, ctx, invites, orgID, tenantID, email, invTTL)
		if _, err := priv.Exec(ctx,
			`UPDATE invitation SET expires_at = now() - interval '1 second' WHERE invitation_id = $1`,
			inv.InvitationID); err != nil {
			t.Fatal(err)
		}
		if _, err := redeem.Redeem(ctx, token, ""); !errors.Is(err, domain.ErrTokenNotRedeemable) {
			t.Fatalf("expired: got %v, want ErrTokenNotRedeemable", err)
		}
		assertNothingHappened(t, priv, orgID, email)
	})

	t.Run("revoked", func(t *testing.T) {
		priv, invites, redeem, tenantID, orgID := redemptionFixture(t, "rev")
		ctx := adminTestCtx()
		const email = "revoked-redeem@example.com"
		cleanupUser(t, priv, email)

		inv, token := issue(t, ctx, invites, orgID, tenantID, email, invTTL)
		if _, err := invites.Revoke(ctx, inv.InvitationID); err != nil {
			t.Fatal(err)
		}
		if _, err := redeem.Redeem(ctx, token, ""); !errors.Is(err, domain.ErrTokenNotRedeemable) {
			t.Fatalf("revoked: got %v, want ErrTokenNotRedeemable", err)
		}
		assertNothingHappened(t, priv, orgID, email)
	})

	t.Run("unknown", func(t *testing.T) {
		priv, invites, redeem, tenantID, orgID := redemptionFixture(t, "unknown")
		ctx := adminTestCtx()
		const email = "unknown-redeem@example.com"
		cleanupUser(t, priv, email)

		_, _, err := domain.NewInvitation(orgID, tenantID, email, invTTL, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_, never, err := domain.NewInvitation(orgID, tenantID, email, invTTL, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		_ = invites
		if _, err := redeem.Redeem(ctx, never, ""); !errors.Is(err, domain.ErrTokenNotRedeemable) {
			t.Fatalf("unknown token: got %v, want ErrTokenNotRedeemable", err)
		}
		if _, err := redeem.Redeem(ctx, "", ""); !errors.Is(err, domain.ErrTokenNotRedeemable) {
			t.Fatalf("empty token: got %v, want ErrTokenNotRedeemable", err)
		}
		assertNothingHappened(t, priv, orgID, email)
	})
}

// assertNothingHappened proves a refused redemption wrote nothing at all.
func assertNothingHappened(t *testing.T, priv *pgxpool.Pool, orgID, email string) {
	t.Helper()
	ctx := adminTestCtx()
	var memberships, users, redeemed int
	if err := priv.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM membership WHERE org_id = $1),
		       (SELECT count(*) FROM app_user WHERE lower(email::text) = $2),
		       (SELECT count(*) FROM invitation WHERE org_id = $1 AND status = 'redeemed')`,
		orgID, domain.NormalizeEmail(email)).Scan(&memberships, &users, &redeemed); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if memberships != 0 || users != 0 || redeemed != 0 {
		t.Errorf("a refused redemption left memberships=%d users=%d redeemed=%d, want 0/0/0",
			memberships, users, redeemed)
	}
}

// Atomicity, proven by breaking the last step.
//
// The membership insert is made to fail by dropping the organisation's row
// out from under it — a foreign-key violation at the final statement. The
// invitation must still be pending and no user may exist: the transaction is
// what makes "the token is consumed" and "the membership exists" the same fact.
func TestRedemption_IsAtomicWhenTheLastStepFails(t *testing.T) {
	priv, invites, redeem, tenantID, orgID := redemptionFixture(t, "atomic")
	ctx := adminTestCtx()
	const email = "atomic@example.com"
	cleanupUser(t, priv, email)

	inv, token := issue(t, ctx, invites, orgID, tenantID, email, invTTL)

	// A membership referencing an org_id that does not exist cannot be written.
	// Rewriting the invitation's org_id makes step 3 fail while steps 1 and 2
	// succeed, which is exactly the partial state the transaction must prevent.
	if _, err := priv.Exec(ctx,
		`UPDATE invitation SET org_id = org_id WHERE invitation_id = $1`, inv.InvitationID); err != nil {
		t.Fatal(err)
	}
	if _, err := priv.Exec(ctx,
		`ALTER TABLE membership DROP CONSTRAINT IF EXISTS membership_org_id_fkey`); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// Restore the constraint no matter how this test exits.
	t.Cleanup(func() {
		c := context.Background()
		_, _ = priv.Exec(c, `DELETE FROM membership WHERE org_id = $1`, orgID)
		_, _ = priv.Exec(c, `ALTER TABLE membership
			ADD CONSTRAINT membership_org_id_fkey FOREIGN KEY (org_id) REFERENCES organization (org_id)`)
	})
	// With the FK gone, force the failure with a check the row cannot satisfy.
	if _, err := priv.Exec(ctx,
		`ALTER TABLE membership ADD CONSTRAINT ck_atomic_probe CHECK (org_id <> '`+orgID+`')`); err != nil {
		t.Fatalf("install probe constraint: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		_, _ = priv.Exec(c, `ALTER TABLE membership DROP CONSTRAINT IF EXISTS ck_atomic_probe`)
	})

	if _, err := redeem.Redeem(ctx, token, "Atomic"); err == nil {
		t.Fatal("Redeem succeeded although the membership write could not")
	}

	// Nothing survived.
	after, err := invites.Get(ctx, inv.InvitationID)
	if err != nil {
		t.Fatalf("Get invitation: %v", err)
	}
	if after.Status != domain.InvitationPending {
		t.Errorf("invitation is %q after a failed redemption, want pending — the token was "+
			"burned by a redemption that granted nothing", after.Status)
	}
	var users int
	if err := priv.QueryRow(ctx, `SELECT count(*) FROM app_user WHERE lower(email::text) = $1`,
		email).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 0 {
		t.Errorf("%d accounts survived a failed redemption, want 0", users)
	}
}

// Redemption must NOT run on the tenant-scoped pool: the invitee holds no
// membership, so there is no tenant to bind before the invitation is found.
//
// Stating the constraint as an executable fact means a later change that wires
// the redeem path to the scoped pool fails here, rather than in production as
// "every redemption is refused".
func TestRedemption_CannotRunOnTheScopedPool(t *testing.T) {
	priv, invites, _, tenantID, orgID := redemptionFixture(t, "scoped")
	ctx := adminTestCtx()
	const email = "scoped-redeem@example.com"
	cleanupUser(t, priv, email)

	inv, token := issue(t, ctx, invites, orgID, tenantID, email, invTTL)

	scoped := NewRedemptionStore(tenantScopedPool(t))
	if _, err := scoped.Redeem(ctx, token, "Scoped"); !errors.Is(err, domain.ErrTokenNotRedeemable) {
		t.Fatalf("scoped Redeem: got %v, want ErrTokenNotRedeemable — redemption on a "+
			"tenant-bound connection must not find the invitation", err)
	}

	// And the refused attempt consumed nothing, so the real path still works.
	after, err := invites.Get(ctx, inv.InvitationID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.InvitationPending {
		t.Errorf("invitation is %q after a scoped attempt, want pending", after.Status)
	}
}

// The membership a redemption writes must be visible to its own tenant and
// invisible to every other — the RLS policy enabled in M5, exercised through
// the row this story creates.
func TestRedemption_MembershipIsConfinedByRowLevelSecurity(t *testing.T) {
	priv, invites, redeem, tenantA, orgA := redemptionFixture(t, "rls-a")
	ctx := adminTestCtx()
	tenantB, _ := seedOrgForInvitations(ctx, t, priv, "rls-b")
	const email = "rls-member@example.com"
	cleanupUser(t, priv, email)

	_, token := issue(t, ctx, invites, orgA, tenantA, email, invTTL)
	got, err := redeem.Redeem(ctx, token, "RLS Member")
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	scoped := tenantScopedPool(t)
	count := func(tenantID string) int {
		t.Helper()
		var n int
		c := domain.WithTenant(ctx, &domain.Tenant{TenantID: tenantID})
		if err := scoped.QueryRow(c,
			`SELECT count(*) FROM membership WHERE membership_id = $1`,
			got.Membership.MembershipID).Scan(&n); err != nil {
			t.Fatalf("scoped read as %s: %v", tenantID, err)
		}
		return n
	}
	if n := count(tenantA); n != 1 {
		t.Errorf("tenant A sees %d of its own memberships, want 1", n)
	}
	if n := count(tenantB); n != 0 {
		t.Errorf("tenant B sees %d of tenant A's memberships, want 0 — row-level security is "+
			"not confining the row redemption created", n)
	}
}
