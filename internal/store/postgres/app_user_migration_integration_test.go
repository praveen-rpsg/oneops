//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
)

// The app_user migration must apply and roll back on a *populated* database, not
// an empty one. A rollback that works only against no rows is a rollback nobody
// can use in the situation it exists for.
//
// The whole cycle runs inside one transaction and is rolled back at the end:
// PostgreSQL has transactional DDL, so the table is dropped and restored without
// the shared test database ever observing it missing. A test that genuinely
// dropped app_user would break every test that ran after it in the same suite.
func TestAppUserMigration_AppliesAndRollsBackOnPopulatedDatabase(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	down, err := os.ReadFile("../migrate/rollback/20260804000003_app_user.down.sql")
	if err != nil {
		t.Fatalf("read rollback script: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Forward migration is already applied by the suite's schema setup.
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('app_user') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("probe app_user: %v", err)
	}
	if !exists {
		t.Fatal("app_user does not exist — the forward migration did not apply")
	}

	// Populate. The rollback must cope with data, which is the only state that
	// makes it worth testing.
	if _, err := tx.Exec(ctx,
		`INSERT INTO app_user (user_id, email, display_name, status)
		 VALUES ($1, $2, $3, $4)`,
		"usr_rollback_probe", "Rollback.Probe@Example.COM", "Rollback Probe", "active"); err != nil {
		t.Fatalf("populate app_user: %v", err)
	}

	// Each expected-failure insert runs inside its own savepoint. A statement that
	// violates a constraint aborts the enclosing transaction in PostgreSQL
	// (SQLSTATE 25P02), so asserting a rejection and then continuing requires one
	// — otherwise the rollback under test can never be reached.
	rejects := func(name, sql string, args ...any) bool {
		t.Helper()
		sp, err := tx.Begin(ctx) // pgx implements a nested Begin as a savepoint
		if err != nil {
			t.Fatalf("savepoint for %s: %v", name, err)
		}
		_, execErr := sp.Exec(ctx, sql, args...)
		_ = sp.Rollback(ctx)
		return execErr != nil
	}

	// citext: the address inserted with mixed case must collide with a differently
	// cased duplicate. This is the property the column type exists for, and it is
	// asserted here rather than trusted.
	if !rejects("duplicate email",
		`INSERT INTO app_user (user_id, email) VALUES ($1, $2)`,
		"usr_rollback_probe_dup", "rollback.probe@example.com") {
		t.Error("a differently cased duplicate email was accepted — uq_user_email is " +
			"not case-insensitive, so the same person can hold two accounts")
	}

	// The lifecycle check must refuse a state outside ADR-IDENTITY-001 §8.3.
	if !rejects("invalid status",
		`INSERT INTO app_user (user_id, email, status) VALUES ($1, $2, $3)`,
		"usr_rollback_probe_bad", "bad.status@example.com", "banished") {
		t.Error("ck_user_status accepted a state outside the ratified lifecycle")
	}

	// membership and invitation reference app_user, so their rollback must run
	// first — PostgreSQL refuses to drop a referenced table, which is the
	// ordering protection documented in 20260804000004's rollback, not an
	// obstacle to work around.
	mDown, err := os.ReadFile("../migrate/rollback/20260804000004_membership.down.sql")
	if err != nil {
		t.Fatalf("read membership rollback: %v", err)
	}
	if _, err := tx.Exec(ctx, string(mDown)); err != nil {
		t.Fatalf("roll back membership before app_user: %v", err)
	}

	// Roll back.
	if _, err := tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("apply rollback on a populated table: %v", err)
	}

	if err := tx.QueryRow(ctx, `SELECT to_regclass('app_user') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("probe app_user after rollback: %v", err)
	}
	if exists {
		t.Error("app_user still exists after its rollback script ran")
	}

	// The rollback deliberately leaves citext in place: it is database-wide and
	// another migration may depend on it (see the script's header).
	var hasCitext bool
	if err := tx.QueryRow(ctx,
		`SELECT count(*) > 0 FROM pg_extension WHERE extname = 'citext'`).Scan(&hasCitext); err != nil {
		t.Fatalf("probe citext: %v", err)
	}
	if !hasCitext {
		t.Error("rollback dropped the citext extension — a database-wide object " +
			"another table may already depend on")
	}
}

// app_user is global by decision (ADR-IDENTITY-001 §8.2): it carries no
// tenant_id and is not tenant-owned. Asserted so that a later change adding
// tenant_id — which would make it a second answer to row ownership — fails here
// rather than in production.
func TestAppUser_IsGlobalNotTenantOwned(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var hasTenantID bool
	if err := pool.QueryRow(ctx,
		`SELECT count(*) > 0 FROM information_schema.columns
		  WHERE table_schema = current_schema()
		    AND table_name   = 'app_user'
		    AND column_name  = 'tenant_id'`).Scan(&hasTenantID); err != nil {
		t.Fatalf("probe tenant_id: %v", err)
	}
	if hasTenantID {
		t.Error("app_user has a tenant_id column. It is global by ADR-IDENTITY-001 §8.2; " +
			"a person exists independently of any membership, and adding an ownership " +
			"column here creates a second answer to who owns a row (CMR-D04)")
	}

	for _, table := range TenantOwnedTables {
		if table == "app_user" {
			t.Error("app_user is registered in TenantOwnedTables. It is global; " +
				"registering it would put it under RLS keyed on a column it does not have, " +
				"and the schema validator would refuse startup")
		}
	}
}
