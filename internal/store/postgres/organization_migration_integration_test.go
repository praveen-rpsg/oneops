//go:build integration

package postgres

import (
	"context"
	"os"
	"testing"
)

// The 1:1 between Organisation and Tenant is the substance of ADR-IDENTITY-001
// §4, and uq_org_tenant is the only thing enforcing it. Everything else in the
// identity model assumes a row has exactly one owner; a second organisation on
// one tenant is the first step toward two answers to "who owns this row"
// (CMR-D04).
func TestOrganization_IsOneToOneWithTenant(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO tenant (tenant_id, slug, name) VALUES ($1, $2, $3)`,
		"tn-oneone", "tn-oneone", "One To One"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO organization (org_id, tenant_id, slug, name) VALUES ($1, $2, $3, $4)`,
		"org_first", "tn-oneone", "org-first", "First"); err != nil {
		t.Fatalf("first organisation rejected: %v", err)
	}

	sp, err := tx.Begin(ctx) // savepoint: the violation below aborts its scope
	if err != nil {
		t.Fatalf("savepoint: %v", err)
	}
	_, err = sp.Exec(ctx,
		`INSERT INTO organization (org_id, tenant_id, slug, name) VALUES ($1, $2, $3, $4)`,
		"org_second", "tn-oneone", "org-second", "Second")
	_ = sp.Rollback(ctx)
	if err == nil {
		t.Error("a second organisation was accepted on a tenant that already had one — " +
			"uq_org_tenant is not enforcing the 1:1 of ADR-IDENTITY-001 §4")
	}
}

// An organisation may not point at a tenant that does not exist. The foreign key
// is what makes tenant_id a pointer to a real boundary rather than a free-text
// label, which is the distinction ADR-TENANCY-001 drew for every other table.
func TestOrganization_RequiresAnExistingTenant(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO organization (org_id, tenant_id, slug, name) VALUES ($1, $2, $3, $4)`,
		"org_orphan", "tn-does-not-exist", "org-orphan", "Orphan"); err == nil {
		t.Error("an organisation was created against a non-existent tenant — the " +
			"foreign key to tenant is missing or deferred")
	}
}

// Constraints that bound the row: the slug grammar must match ck_tenant_slug
// exactly (the backfill copies tenant slugs verbatim), and status must stay
// inside the lifecycle of ADR-IDENTITY-001 §8.3.
func TestOrganization_ConstraintsBoundTheRow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO tenant (tenant_id, slug, name) VALUES ($1, $2, $3)`,
		"tn-bounds", "tn-bounds", "Bounds"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	rejects := func(name string, args ...any) bool {
		t.Helper()
		sp, err := tx.Begin(ctx)
		if err != nil {
			t.Fatalf("savepoint for %s: %v", name, err)
		}
		_, execErr := sp.Exec(ctx,
			`INSERT INTO organization (org_id, tenant_id, slug, name, status)
			 VALUES ($1, $2, $3, $4, $5)`, args...)
		_ = sp.Rollback(ctx)
		return execErr != nil
	}

	if !rejects("uppercase slug", "org_a", "tn-bounds", "Not-Lower", "A", "active") {
		t.Error("ck_org_slug accepted an uppercase slug; ck_tenant_slug would not, " +
			"so an organisation can hold a slug its own tenant could not")
	}
	if !rejects("leading dash", "org_b", "tn-bounds", "-leading", "B", "active") {
		t.Error("ck_org_slug accepted a leading dash")
	}
	if !rejects("bad status", "org_c", "tn-bounds", "org-c", "C", "dissolved") {
		t.Error("ck_org_status accepted a state outside the ratified lifecycle")
	}
}

// The backfill must cover every tenant and must be safe to run twice. A
// migration that only works on a virgin database is not one an operator can use
// during a partial restore.
func TestOrganizationBackfill_CoversEveryTenantAndIsIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	backfill, err := os.ReadFile("../migrate/sql/20260804000002_organization_backfill.sql")
	if err != nil {
		t.Fatalf("read backfill: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// A tenant that arrived after the original backfill ran.
	if _, err := tx.Exec(ctx,
		`INSERT INTO tenant (tenant_id, slug, name) VALUES ($1, $2, $3)`,
		"tn-late", "tn-late", "Late Arrival"); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	if _, err := tx.Exec(ctx, string(backfill)); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var orphans int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM tenant t
		  WHERE NOT EXISTS (SELECT 1 FROM organization o WHERE o.tenant_id = t.tenant_id)`).
		Scan(&orphans); err != nil {
		t.Fatalf("count orphan tenants: %v", err)
	}
	if orphans != 0 {
		t.Errorf("%d tenant(s) have no organisation after the backfill — the 1:1 of "+
			"ADR-IDENTITY-001 §4 would hold only for rows created after the migration", orphans)
	}

	var before int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM organization`).Scan(&before); err != nil {
		t.Fatalf("count organisations: %v", err)
	}

	// Re-run. ON CONFLICT DO NOTHING must make this a no-op, not a violation.
	if _, err := tx.Exec(ctx, string(backfill)); err != nil {
		t.Fatalf("backfill is not re-runnable: %v", err)
	}

	var after int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM organization`).Scan(&after); err != nil {
		t.Fatalf("re-count organisations: %v", err)
	}
	if after != before {
		t.Errorf("re-running the backfill changed the organisation count %d -> %d; it is not idempotent",
			before, after)
	}
}

// Apply and roll back on a populated database, inside one transaction so the
// shared test schema never observes the table missing.
func TestOrganizationMigration_AppliesAndRollsBackOnPopulatedDatabase(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	down, err := os.ReadFile("../migrate/rollback/20260804000001_organization.down.sql")
	if err != nil {
		t.Fatalf("read rollback: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT to_regclass('organization') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("probe organization: %v", err)
	}
	if !exists {
		t.Fatal("organization does not exist — the forward migration did not apply")
	}

	var populated int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM organization`).Scan(&populated); err != nil {
		t.Fatalf("count: %v", err)
	}
	if populated == 0 {
		t.Fatal("organization is empty — the backfill did not run, so the rollback " +
			"would be exercised against no data")
	}

	if _, err := tx.Exec(ctx, string(down)); err != nil {
		t.Fatalf("apply rollback on a populated table: %v", err)
	}

	if err := tx.QueryRow(ctx, `SELECT to_regclass('organization') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("probe after rollback: %v", err)
	}
	if exists {
		t.Error("organization still exists after its rollback script ran")
	}
}

// organization is global by ADR-IDENTITY-002 §3: it holds no tenant_id of its
// own and must not be tenant-owned. Registering it would put it under an RLS
// policy keyed on a column that means something else here, and make the mapping
// unreadable at the moment it is needed to resolve the key.
func TestOrganization_IsGlobalNotTenantOwned(t *testing.T) {
	for _, table := range TenantOwnedTables {
		if table == "organization" {
			t.Error("organization is registered in TenantOwnedTables. It is global " +
				"(ADR-IDENTITY-002 §3.1): tenant_id is discovered by reading this table, " +
				"so gating it on tenant_id is circular")
		}
	}
}
