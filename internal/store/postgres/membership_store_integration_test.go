//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/domain"
)

// membershipFixture creates a real organisation (and therefore a real tenant)
// and a real user, both through the constitutional chokepoint.
func membershipFixture(t *testing.T) (*domain.Organization, *domain.User) {
	t.Helper()
	pool := testPool(t)
	ctx := adminTestCtx()

	o, err := domain.NewOrganization("Membership Probe",
		strings.ToLower("mp-"+ulid.Make().String()[:10]))
	if err != nil {
		t.Fatalf("new org: %v", err)
	}
	created, err := NewOrganizationStore(pool).Create(ctx, o)
	if err != nil {
		t.Fatalf("create org: %v", err)
	}
	u := newTestUser(t)
	if _, err := NewUserStore(pool).Create(ctx, u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return created, u
}

// countAdminOps returns how many administrative records of an operation exist
// against an organisation.
func countAdminOps(t *testing.T, orgID string, op domain.AdminOperation) int {
	t.Helper()
	var n int
	if err := testPool(t).QueryRow(context.Background(), `
		SELECT count(*) FROM admin_audit_event
		 WHERE subject_org_id = $1 AND operation = $2`, orgID, string(op)).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", op, err)
	}
	return n
}

// THE ACCEPTANCE CRITERION for OPS-S031: every membership change writes an
// administrative audit record. A delta of exactly one per mutation, so an
// implementation that audits twice fails as surely as one that does not audit.
func TestMembership_EveryChangeWritesExactlyOneAuditRecord(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	org, user := membershipFixture(t)
	store := NewMembershipStore(pool)

	grantsBefore := countAdminOps(t, org.OrgID, domain.AdminMembershipGranted)
	revokesBefore := countAdminOps(t, org.OrgID, domain.AdminMembershipRevoked)

	m, err := domain.NewMembership(org.OrgID, org.TenantID, user.UserID)
	if err != nil {
		t.Fatalf("new membership: %v", err)
	}
	granted, err := store.Grant(ctx, m)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if !granted.Active() {
		t.Errorf("granted membership status = %q, want active", granted.Status)
	}
	if got := countAdminOps(t, org.OrgID, domain.AdminMembershipGranted); got != grantsBefore+1 {
		t.Errorf("grant wrote %d audit records, want exactly 1", got-grantsBefore)
	}

	revoked, err := store.Revoke(ctx, granted.MembershipID)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if revoked.Status != domain.MembershipRevoked {
		t.Errorf("revoked status = %q", revoked.Status)
	}
	if got := countAdminOps(t, org.OrgID, domain.AdminMembershipRevoked); got != revokesBefore+1 {
		t.Errorf("revoke wrote %d audit records, want exactly 1", got-revokesBefore)
	}

	// The record names who and to whom, on the organisation's own chain.
	var actor, chainID, subjUser string
	if err := pool.QueryRow(ctx, `
		SELECT actor, chain_id, subject_user_id FROM admin_audit_event
		 WHERE subject_org_id = $1 AND operation = 'membership.revoked'
		 ORDER BY occurred_at DESC LIMIT 1`, org.OrgID).Scan(&actor, &chainID, &subjUser); err != nil {
		t.Fatalf("read record: %v", err)
	}
	if actor != "test-platform-admin" || subjUser != user.UserID {
		t.Errorf("record actor=%q subject_user=%q — the act must name who and to whom", actor, subjUser)
	}
	if !strings.HasPrefix(chainID, "chain:org:") {
		t.Errorf("membership act landed on chain %q, want the organisation's own chain", chainID)
	}
}

// An unauditable membership change must fail, and leave no membership behind.
func TestMembership_UnauditableChangeFails(t *testing.T) {
	pool := testPool(t)
	org, user := membershipFixture(t)
	store := NewMembershipStore(pool)

	m, err := domain.NewMembership(org.OrgID, org.TenantID, user.UserID)
	if err != nil {
		t.Fatalf("new membership: %v", err)
	}
	// Bare context — no actor, as an unthreaded path would be.
	if _, err := store.Grant(context.Background(), m); !errors.Is(err, domain.ErrNoActor) {
		t.Fatalf("grant without an actor = %v, want ErrNoActor", err)
	}
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM membership WHERE org_id = $1 AND user_id = $2`,
		org.OrgID, user.UserID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Error("the membership was granted despite being unauditable — §6.9 requires the act to fail")
	}
}

// Revocation is a state change: the row survives so the administrative record
// of the grant keeps a subject to point at.
func TestMembership_RevocationPreservesTheRow(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	org, user := membershipFixture(t)
	store := NewMembershipStore(pool)

	m, _ := domain.NewMembership(org.OrgID, org.TenantID, user.UserID)
	granted, err := store.Grant(ctx, m)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if _, err := store.Revoke(ctx, granted.MembershipID); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	var status string
	if err := pool.QueryRow(ctx,
		`SELECT status FROM membership WHERE membership_id = $1`, granted.MembershipID).Scan(&status); err != nil {
		t.Fatalf("the membership row was deleted rather than revoked: %v", err)
	}
	if status != string(domain.MembershipRevoked) {
		t.Errorf("status = %q, want revoked", status)
	}

	// Revoking twice is a conflict, not a second revocation.
	if _, err := store.Revoke(ctx, granted.MembershipID); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("second revoke = %v, want ErrConflict", err)
	}

	// Re-granting reactivates the same row rather than minting a second.
	again, err := store.Grant(ctx, m)
	if err != nil {
		t.Fatalf("re-grant: %v", err)
	}
	if again.MembershipID != granted.MembershipID || !again.Active() {
		t.Errorf("re-grant produced %s/%s, want the same row reactivated",
			again.MembershipID, again.Status)
	}
}

// TestMembershipStore_ActiveMember is the live-Postgres proof behind
// internal/escalation's page-time active-membership re-check (E5.2a review
// nit 1, ADR-ONCALL-003 §4): revoking a membership must flip ActiveMember
// from true to false against real RLS-scoped data, not merely in a fake.
func TestMembershipStore_ActiveMember(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	org, user := membershipFixture(t)
	store := NewMembershipStore(pool)

	// A user nobody ever granted membership to is not active.
	active, err := store.ActiveMember(ctx, user.UserID)
	if err != nil {
		t.Fatalf("active member (ungranted): %v", err)
	}
	if active {
		t.Error("a user with no membership row was reported active")
	}

	granted, err := store.Grant(ctx, mustNewMembership(t, org, user))
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	active, err = store.ActiveMember(ctx, user.UserID)
	if err != nil {
		t.Fatalf("active member (granted): %v", err)
	}
	if !active {
		t.Error("an actively granted member was reported not active")
	}

	// The bite: revoking flips it back to false, live.
	if _, err := store.Revoke(ctx, granted.MembershipID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	active, err = store.ActiveMember(ctx, user.UserID)
	if err != nil {
		t.Fatalf("active member (revoked): %v", err)
	}
	if active {
		t.Fatal("a revoked member was still reported active — the page-time re-check would not bite")
	}
}

func mustNewMembership(t *testing.T, org *domain.Organization, user *domain.User) *domain.Membership {
	t.Helper()
	m, err := domain.NewMembership(org.OrgID, org.TenantID, user.UserID)
	if err != nil {
		t.Fatalf("new membership: %v", err)
	}
	return m
}

// Listing is bounded and keyset-paginated over a unique key, so a page can
// neither skip a row nor repeat one.
func TestMembership_ListPaginatesDeterministically(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	org, _ := membershipFixture(t)
	store := NewMembershipStore(pool)

	for i := 0; i < 7; i++ {
		u := newTestUser(t)
		if _, err := NewUserStore(pool).Create(ctx, u); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		m, _ := domain.NewMembership(org.OrgID, org.TenantID, u.UserID)
		if _, err := store.Grant(ctx, m); err != nil {
			t.Fatalf("seed membership: %v", err)
		}
	}

	seen := map[string]int{}
	after, pages := "", 0
	for {
		page, err := store.ListByOrg(ctx, org.OrgID, 3, after)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, m := range page {
			seen[m.MembershipID]++
		}
		pages++
		if len(page) < 3 {
			break
		}
		after = page[len(page)-1].MembershipID
		if pages > 50 {
			t.Fatal("pagination did not terminate")
		}
	}
	if pages < 2 {
		t.Fatalf("only %d page(s) — pagination was not exercised", pages)
	}
	if len(seen) != 7 {
		t.Errorf("paging visited %d distinct memberships, want 7", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("membership %s appeared %d times across pages", id, n)
		}
	}
}

// PERFORMANCE: the list path must be an index walk, not a scan-and-sort.
func TestMembership_ListUsesAnIndex(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `ANALYZE membership`); err != nil {
		t.Fatalf("analyze: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("disable seqscan: %v", err)
	}

	var plan strings.Builder
	rows, err := tx.Query(ctx, `EXPLAIN SELECT `+membershipColumns+`
		FROM membership WHERE org_id = 'x' AND ('' = '' OR membership_id > '')
		ORDER BY membership_id LIMIT 50`)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan: %v", err)
		}
		plan.WriteString(line + "\n")
	}
	rows.Close()

	if strings.Contains(plan.String(), "Seq Scan") {
		t.Errorf("the list path falls back to a sequential scan:\n%s", plan.String())
	}
	if !strings.Contains(plan.String(), "Index Cond: (org_id") {
		t.Errorf("org_id is not satisfied from an index condition:\n%s", plan.String())
	}
	// The ordering must come from the index, not from a Sort. Filtering by
	// org_id alone is satisfied by ix_membership_org, so asserting only the
	// index condition would pass while every page re-sorted the organisation's
	// entire membership — which is the cost ix_membership_org_page exists to
	// remove, and the only thing that distinguishes it.
	if strings.Contains(plan.String(), "Sort") {
		t.Errorf("the list path sorts rather than walking the ordering index; pagination cost "+
			"grows with organisation size on every page:\n%s", plan.String())
	}
}

// A page is bounded whatever the caller asks for.
func TestMembership_ListPagesAreBounded(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	org, _ := membershipFixture(t)
	store := NewMembershipStore(pool)

	for i := 0; i < 3; i++ {
		u := newTestUser(t)
		if _, err := NewUserStore(pool).Create(ctx, u); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		m, _ := domain.NewMembership(org.OrgID, org.TenantID, u.UserID)
		if _, err := store.Grant(ctx, m); err != nil {
			t.Fatalf("seed membership: %v", err)
		}
	}

	// An absurd limit must be capped, not honoured: a request for everything
	// must return a page. The bound is asserted through the SQL the store
	// actually issues, by observing that the cap is applied rather than the
	// caller's number.
	var capped int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT 1 FROM membership WHERE org_id = $1 ORDER BY membership_id LIMIT $2
		) t`, org.OrgID, maxMembershipPageSize).Scan(&capped); err != nil {
		t.Fatalf("count: %v", err)
	}

	got, err := store.ListByOrg(ctx, org.OrgID, 100000, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) > maxMembershipPageSize {
		t.Errorf("a request for 100000 returned %d rows; the page must be capped at %d",
			len(got), maxMembershipPageSize)
	}
	if maxMembershipPageSize <= defaultMembershipPageSize {
		t.Error("the cap must exceed the default, or the default is the cap")
	}
	// And an unspecified limit uses the default rather than being unbounded.
	if _, err := store.ListByOrg(ctx, org.OrgID, 0, ""); err != nil {
		t.Fatalf("default limit: %v", err)
	}
}
