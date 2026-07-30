//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// seedIdentity creates a tenant, its organisation and a user inside tx, and
// returns the ids. Every test here needs the same three rows because membership
// references all of them — which is itself the point of TestMembership_RequiresAllThreeReferences.
func seedIdentity(ctx context.Context, t *testing.T, tx pgx.Tx, suffix string) (tenantID, orgID, userID string) {
	t.Helper()
	tenantID = "tn-" + suffix
	orgID = "org_" + suffix
	userID = "usr_" + suffix
	if _, err := tx.Exec(ctx,
		`INSERT INTO tenant (tenant_id, slug, name) VALUES ($1, $1, $2)`, tenantID, "T "+suffix); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO organization (org_id, tenant_id, slug, name) VALUES ($1, $2, $3, $4)`,
		orgID, tenantID, "org-"+suffix, "Org "+suffix); err != nil {
		t.Fatalf("seed organization: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO app_user (user_id, email) VALUES ($1, $2)`,
		userID, suffix+"@example.com"); err != nil {
		t.Fatalf("seed app_user: %v", err)
	}
	return tenantID, orgID, userID
}

// membership points at three real rows. The foreign keys are what make tenant_id
// an ownership key rather than a free-text label, and org_id/user_id a
// relationship rather than a pair of strings that happen to look like ids.
func TestMembership_RequiresAllThreeReferences(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tenantID, orgID, userID := seedIdentity(ctx, t, tx, "refs")

	rejects := func(name, tn, org, usr string) bool {
		t.Helper()
		sp, err := tx.Begin(ctx)
		if err != nil {
			t.Fatalf("savepoint for %s: %v", name, err)
		}
		_, execErr := sp.Exec(ctx,
			`INSERT INTO membership (membership_id, tenant_id, org_id, user_id)
			 VALUES ($1, $2, $3, $4)`, "mbr_"+name, tn, org, usr)
		_ = sp.Rollback(ctx)
		return execErr != nil
	}

	if !rejects("badtenant", "tn-nope", orgID, userID) {
		t.Error("membership accepted a non-existent tenant — tenant_id is not a real " +
			"ownership key, only a string")
	}
	if !rejects("badorg", tenantID, "org_nope", userID) {
		t.Error("membership accepted a non-existent organisation")
	}
	if !rejects("baduser", tenantID, orgID, "usr_nope") {
		t.Error("membership accepted a non-existent user")
	}

	// The valid combination must be accepted, or the rejections above prove nothing.
	if _, err := tx.Exec(ctx,
		`INSERT INTO membership (membership_id, tenant_id, org_id, user_id)
		 VALUES ($1, $2, $3, $4)`, "mbr_ok", tenantID, orgID, userID); err != nil {
		t.Fatalf("a fully valid membership was rejected: %v", err)
	}
}

// One membership per user per organisation. Revocation is a status change, so a
// second row would mean a revoked membership and a live one coexisting, and
// "is this user a member" would have two answers.
func TestMembership_IsSingularPerUserPerOrganisation(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tenantID, orgID, userID := seedIdentity(ctx, t, tx, "singular")

	if _, err := tx.Exec(ctx,
		`INSERT INTO membership (membership_id, tenant_id, org_id, user_id)
		 VALUES ($1, $2, $3, $4)`, "mbr_one", tenantID, orgID, userID); err != nil {
		t.Fatalf("first membership rejected: %v", err)
	}

	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	_, err = sp.Exec(ctx,
		`INSERT INTO membership (membership_id, tenant_id, org_id, user_id)
		 VALUES ($1, $2, $3, $4)`, "mbr_two", tenantID, orgID, userID)
	_ = sp.Rollback(ctx)
	if err == nil {
		t.Error("a second membership was accepted for the same user in the same " +
			"organisation — uq_membership_org_user is not enforcing singularity, so " +
			"\"is this user a member\" has two answers")
	}
}

// Lifecycle states are bounded by ADR-IDENTITY-001 §8.3 on both tables.
func TestMembershipAndInvitation_LifecyclesAreBounded(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tenantID, orgID, userID := seedIdentity(ctx, t, tx, "lifecycle")

	rejects := func(name, sql string, args ...any) bool {
		t.Helper()
		sp, err := tx.Begin(ctx)
		if err != nil {
			t.Fatalf("savepoint for %s: %v", name, err)
		}
		_, execErr := sp.Exec(ctx, sql, args...)
		_ = sp.Rollback(ctx)
		return execErr != nil
	}

	if !rejects("membership status",
		`INSERT INTO membership (membership_id, tenant_id, org_id, user_id, status)
		 VALUES ($1, $2, $3, $4, $5)`, "mbr_bad", tenantID, orgID, userID, "banned") {
		t.Error("ck_membership_status accepted a state outside active|revoked")
	}
	if !rejects("invitation status",
		`INSERT INTO invitation (invitation_id, tenant_id, org_id, email, token_hash, status, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, now() + interval '1 day')`,
		"inv_bad", tenantID, orgID, "x@example.com", "hash-bad", "accepted") {
		t.Error("ck_invitation_status accepted a state outside the ratified lifecycle")
	}
}

// An invitation token is a bearer credential: the hash is unique, so the same
// token cannot be issued twice, and the email is case-insensitive like app_user's.
func TestInvitation_TokenIsSingularAndEmailIsCaseInsensitive(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tenantID, orgID, _ := seedIdentity(ctx, t, tx, "invtoken")

	if _, err := tx.Exec(ctx,
		`INSERT INTO invitation (invitation_id, tenant_id, org_id, email, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4, $5, now() + interval '1 day')`,
		"inv_one", tenantID, orgID, "Invitee@Example.COM", "hash-shared"); err != nil {
		t.Fatalf("first invitation rejected: %v", err)
	}

	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	_, err = sp.Exec(ctx,
		`INSERT INTO invitation (invitation_id, tenant_id, org_id, email, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4, $5, now() + interval '1 day')`,
		"inv_two", tenantID, orgID, "other@example.com", "hash-shared")
	_ = sp.Rollback(ctx)
	if err == nil {
		t.Error("two invitations were issued with the same token hash — uq_invitation_token " +
			"is missing, so one token redeems two invitations")
	}

	// citext's `=` operator lives in public and is found through search_path; an
	// operator cannot be schema-qualified. This pool pins search_path to the test
	// schema alone, so without the line below PostgreSQL silently falls back to
	// text equality and the lookup misses — see the note in 20260804000004.
	//
	// The control-plane sets no search_path and therefore gets the default, which
	// includes public. Setting it here makes the test exercise what production
	// actually runs, rather than a topology only the tests have.
	if _, err := tx.Exec(ctx, `SET LOCAL search_path = pgstore_itest, public`); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	var found int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM invitation WHERE email = $1`,
		"invitee@example.com").Scan(&found); err != nil {
		t.Fatalf("case-insensitive lookup: %v", err)
	}
	if found != 1 {
		t.Errorf("a differently cased address did not match the stored invitation "+
			"(found %d) — invitation.email is not behaving as citext, so redemption "+
			"depends on the casing the invitee happens to type", found)
	}
}

// Both tables must be under forced RLS with a policy — the state the schema
// validator requires of everything in TenantOwnedTables, asserted directly so a
// migration that registers a table without protecting it fails here.
func TestMembershipAndInvitation_AreUnderForcedRLS(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	for _, table := range []string{"membership", "invitation"} {
		var enabled, forced bool
		var policies int
		if err := pool.QueryRow(ctx, `
			SELECT c.relrowsecurity, c.relforcerowsecurity,
			       (SELECT count(*) FROM pg_policies p
			         WHERE p.schemaname = current_schema() AND p.tablename = c.relname)
			  FROM pg_class c
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = current_schema() AND c.relname = $1`, table).
			Scan(&enabled, &forced, &policies); err != nil {
			t.Fatalf("probe %s: %v", table, err)
		}
		if !enabled {
			t.Errorf("%s does not have row-level security enabled — tenant isolation is off", table)
		}
		if !forced {
			t.Errorf("%s does not FORCE row-level security — the owning role bypasses isolation", table)
		}
		if policies == 0 {
			t.Errorf("%s has no row-level-security policy", table)
		}
	}
}

// Apply and roll back on a populated database, in one transaction so the shared
// schema never observes the tables missing.
func TestMembershipMigration_AppliesAndRollsBackOnPopulatedDatabase(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	down, err := os.ReadFile("../migrate/rollback/20260804000004_membership.down.sql")
	if err != nil {
		t.Fatalf("read rollback: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, table := range []string{"membership", "invitation"} {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("probe %s: %v", table, err)
		}
		if !exists {
			t.Fatalf("%s does not exist — the forward migration did not apply", table)
		}
	}

	tenantID, orgID, userID := seedIdentity(ctx, t, tx, "rollback")
	if _, err := tx.Exec(ctx,
		`INSERT INTO membership (membership_id, tenant_id, org_id, user_id)
		 VALUES ($1, $2, $3, $4)`, "mbr_rb", tenantID, orgID, userID); err != nil {
		t.Fatalf("populate membership: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO invitation (invitation_id, tenant_id, org_id, email, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4, $5, now() + interval '1 day')`,
		"inv_rb", tenantID, orgID, "rb@example.com", "hash-rb"); err != nil {
		t.Fatalf("populate invitation: %v", err)
	}

	if _, err := tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("apply rollback on populated tables: %v", err)
	}

	for _, table := range []string{"membership", "invitation"} {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("probe %s after rollback: %v", table, err)
		}
		if exists {
			t.Errorf("%s still exists after its rollback script ran", table)
		}
	}

	// organization and app_user must survive: the rollback removes the tables
	// that reference them, not the tables they reference.
	for _, table := range []string{"organization", "app_user"} {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("probe %s: %v", table, err)
		}
		if !exists {
			t.Errorf("%s was dropped by the membership rollback — it cascaded through a "+
				"foreign key instead of stopping at it", table)
		}
	}
}

// The rollback of a referenced table must be refused while a referencing table
// still holds rows. That refusal is the ordering protection: it is what stops an
// operator rolling back organization and silently taking memberships with it.
func TestMembership_BlocksTheOrganizationRollbackUntilItIsGone(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	orgDown, err := os.ReadFile("../migrate/rollback/20260804000001_organization.down.sql")
	if err != nil {
		t.Fatalf("read organization rollback: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tenantID, orgID, userID := seedIdentity(ctx, t, tx, "ordering")
	if _, err := tx.Exec(ctx,
		`INSERT INTO membership (membership_id, tenant_id, org_id, user_id)
		 VALUES ($1, $2, $3, $4)`, "mbr_ord", tenantID, orgID, userID); err != nil {
		t.Fatalf("populate membership: %v", err)
	}

	sp, err := tx.Begin(ctx)
	if err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	_, err = sp.Exec(ctx, string(orgDown))
	_ = sp.Rollback(ctx)
	if err == nil {
		t.Error("organization was dropped while membership still referenced it — the " +
			"foreign key is not protecting the rollback order, so rolling back M1 " +
			"before M4 would destroy membership data")
	}
}
