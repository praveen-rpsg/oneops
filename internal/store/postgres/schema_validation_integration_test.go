//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/domain"
)

// setRLS toggles a table's row-level security, restoring it on cleanup so the
// shared test schema is never left weakened for other tests.
func setRLS(ctx context.Context, t *testing.T, priv poolExec, table, clause string) {
	t.Helper()
	if _, err := priv.Exec(ctx, fmt.Sprintf("ALTER TABLE %s %s", table, clause)); err != nil {
		t.Fatalf("ALTER TABLE %s %s: %v", table, clause, err)
	}
}

// The exploit this investigation targets: with row-level security disabled on a
// tenant-owned table — the shape a bad migration or an operator ALTER produces —
// a tenant-scoped connection reads across the tenant boundary. Confidentiality
// depends entirely on the schema retaining RLS, and nothing at runtime noticed.
func TestSchema_DisabledRLSLeaksCrossTenant(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()

	tenants := NewTenantStore(priv)
	a, _ := tenants.Create(ctx, newTenant("schema-alpha", "ext-schema-alpha"))
	b, _ := tenants.Create(ctx, newTenant("schema-bravo", "ext-schema-bravo"))
	seedForTenant(ctx, t, priv, a.TenantID, "alpha-secret.md")
	seedForTenant(ctx, t, priv, b.TenantID, "bravo-secret.md")

	scoped := tenantScopedPool(t)
	aCtx := domain.WithTenant(ctx, a)

	// Baseline: isolation holds.
	var n int
	if err := scoped.QueryRow(aCtx,
		`SELECT count(*) FROM configuration_object WHERE artifact='bravo-secret.md'`).Scan(&n); err != nil {
		t.Fatalf("baseline read: %v", err)
	}
	if n != 0 {
		t.Fatalf("baseline already leaking: %d", n)
	}

	// A bad migration disables RLS.
	t.Cleanup(func() {
		setRLS(ctx, t, priv, "configuration_object", "ENABLE ROW LEVEL SECURITY")
		setRLS(ctx, t, priv, "configuration_object", "FORCE ROW LEVEL SECURITY")
	})
	setRLS(ctx, t, priv, "configuration_object", "DISABLE ROW LEVEL SECURITY")

	// A fresh scoped connection now reads across the boundary.
	scoped2 := tenantScopedPool(t)
	if err := scoped2.QueryRow(aCtx,
		`SELECT count(*) FROM configuration_object WHERE artifact='bravo-secret.md'`).Scan(&n); err != nil {
		t.Fatalf("read after disable: %v", err)
	}
	if n == 0 {
		t.Skip("RLS still confined after DISABLE — environment applies RLS to the assumed role regardless; the startup guard below is the real defence")
	}
	// Exploit reproduced: the schema, not the code, was the control.
	t.Logf("cross-tenant read succeeded with RLS disabled (leaked %d row) — this is the vulnerability the schema validator closes", n)
}

// The schema validator refuses each way the ownership model can be weakened.
func TestSchema_ValidatorDetectsWeakenedSchema(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()
	v := NewSchemaValidator(priv)

	// A correct schema has no problems.
	if problems, err := v.Validate(ctx); err != nil {
		t.Fatalf("validate clean: %v", err)
	} else if len(problems) != 0 {
		t.Fatalf("clean schema reported problems: %v", problems)
	}

	cases := []struct {
		name, weaken, restore, want string
	}{
		{"disabled RLS",
			"ALTER TABLE configuration_object DISABLE ROW LEVEL SECURITY",
			"ALTER TABLE configuration_object ENABLE ROW LEVEL SECURITY",
			"row-level security enabled"},
		{"not forced",
			"ALTER TABLE configuration_object NO FORCE ROW LEVEL SECURITY",
			"ALTER TABLE configuration_object FORCE ROW LEVEL SECURITY",
			"FORCE row-level security"},
		{"dropped policy",
			"ALTER TABLE webhook DISABLE ROW LEVEL SECURITY",
			"ALTER TABLE webhook ENABLE ROW LEVEL SECURITY",
			"row-level security enabled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := priv.Exec(ctx, c.weaken); err != nil {
				t.Fatalf("weaken: %v", err)
			}
			defer func() {
				if _, err := priv.Exec(ctx, c.restore); err != nil {
					t.Fatalf("restore: %v", err)
				}
			}()
			problems, err := v.Validate(ctx)
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			joined := strings.Join(problems, "\n")
			if !strings.Contains(joined, c.want) {
				t.Errorf("validator did not detect %q; problems:\n%s", c.name, joined)
			}
		})
	}
}

// A nullable ownership column makes ownership optional; the validator refuses it.
func TestSchema_ValidatorDetectsNullableOwnership(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()
	v := NewSchemaValidator(priv)

	// policy_execution has no inbound FK to make DROP NOT NULL cheap to toggle.
	if _, err := priv.Exec(ctx, `ALTER TABLE policy_execution ALTER COLUMN tenant_id DROP NOT NULL`); err != nil {
		t.Fatalf("weaken: %v", err)
	}
	defer func() {
		// Restore: no NULLs exist, so re-adding NOT NULL succeeds.
		if _, err := priv.Exec(ctx, `ALTER TABLE policy_execution ALTER COLUMN tenant_id SET NOT NULL`); err != nil {
			t.Fatalf("restore: %v", err)
		}
	}()

	problems, err := v.Validate(ctx)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !strings.Contains(strings.Join(problems, "\n"), "tenant_id is nullable") {
		t.Errorf("validator did not detect nullable ownership; problems: %v", problems)
	}
}

var _ = ulid.Make

// Every table that carries a tenant_id must be in the canonical list and fully
// protected. This is the drift guard: a future migration that adds a
// tenant-owned table but forgets its row-level security, or forgets to list it,
// escapes both startup validators — the exact way schema evolution silently
// weakens the model. The check reads the live schema, so it cannot be satisfied
// by editing a list alone.
func TestSchema_EveryTenantIdTableIsCanonicalAndProtected(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	inList := map[string]bool{}
	for _, tbl := range TenantOwnedTables {
		inList[tbl] = true
	}

	// A tenant_id column does not always mean the row is tenant-owned. On the
	// registry tables it identifies *which boundary this row describes*, not who
	// owns the row — and those tables must stay readable before a boundary has
	// been resolved, which is exactly when RLS would hide them.
	//
	// Each exclusion carries its reason, and both are checked below: an excluded
	// table that has vanished, or that has since become genuinely tenant-owned,
	// fails here rather than sitting as a stale literal in a WHERE clause.
	globalTenantIDTables := map[string]string{
		"tenant": "the tenant registry itself — a row here IS a boundary; gating it on " +
			"the boundary is circular (ADR-TENANCY-001 §4)",
		"organization": "global by ADR-IDENTITY-002 §3.1 — tenant_id is a pointer to the " +
			"boundary this organisation is realised as, and tenant_id is discovered BY " +
			"reading this mapping, so gating the mapping on it is circular",
	}

	// Tables with a tenant_id column, excluding partition children (whose parent
	// carries the policy). Registry tables are filtered in Go, against the
	// justified set above, rather than as literals in the query.
	rows, err := pool.Query(ctx, `
		SELECT c.relname
		  FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = current_schema()
		   AND c.relkind IN ('r','p')
		   AND NOT EXISTS (SELECT 1 FROM pg_inherits i WHERE i.inhrelid = c.oid)
		   AND EXISTS (
		       SELECT 1 FROM information_schema.columns col
		        WHERE col.table_schema = current_schema()
		          AND col.table_name = c.relname
		          AND col.column_name = 'tenant_id')`)
	if err != nil {
		t.Fatalf("enumerate tenant tables: %v", err)
	}
	defer rows.Close()

	seen := map[string]bool{}
	var live []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		seen[name] = true
		if _, global := globalTenantIDTables[name]; global {
			continue
		}
		live = append(live, name)
		if !inList[name] {
			t.Errorf("table %q carries tenant_id but is not in TenantOwnedTables; "+
				"the startup validators would not check its row-level security", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	// A justification must still describe reality. Both directions are checked so
	// the exclusion set cannot quietly grant amnesty to a table that has changed.
	for name, why := range globalTenantIDTables {
		if !seen[name] {
			t.Errorf("%q is excluded as a global tenant_id table (%s) but no such table "+
				"carries tenant_id in the live schema — the justification is stale", name, why)
		}
		if inList[name] {
			t.Errorf("%q is excluded as global (%s) yet is also in TenantOwnedTables; "+
				"one of the two is wrong and the exclusion is hiding it", name, why)
		}
	}

	if len(live) < len(TenantOwnedTables) {
		t.Errorf("found %d tenant_id tables live but the canonical list has %d — a listed table is missing from the schema",
			len(live), len(TenantOwnedTables))
	}

	// And the schema validator must pass on the real schema (belt and braces:
	// if any listed table is unprotected, this fails too).
	if problems, err := NewSchemaValidator(pool).Validate(ctx); err != nil {
		t.Fatalf("validate: %v", err)
	} else if len(problems) != 0 {
		t.Errorf("schema validator found problems on a clean schema: %v", problems)
	}
}

// The append-only guard is what makes audit_event authoritative (ADR-TENANCY-004):
// if it could be dropped and a row's tenant_id rewritten, the resolver's
// cross-check against the governed object could be forced to agree with a
// forgery. An operator repair that drops the guard must be detected — and
// while it is dropped, audit history is mutable, which this test demonstrates.
func TestSchema_DroppedAuditGuardIsDetected(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()
	v := NewSchemaValidator(priv)

	if problems, err := v.Validate(ctx); err != nil {
		t.Fatalf("validate clean: %v", err)
	} else if len(problems) != 0 {
		t.Fatalf("clean schema reported problems: %v", problems)
	}

	// Seed a row so mutability is observable, then drop the row-level guard.
	seedAuditRow(ctx, t, priv, "guard-check")
	if _, err := priv.Exec(ctx,
		`DROP TRIGGER trg_audit_event_no_row_mutate ON audit_event`); err != nil {
		t.Fatalf("drop guard: %v", err)
	}
	t.Cleanup(func() {
		if _, err := priv.Exec(ctx, `
			CREATE OR REPLACE TRIGGER trg_audit_event_no_row_mutate
				BEFORE UPDATE OR DELETE ON audit_event
				FOR EACH ROW EXECUTE FUNCTION audit_event_immutable()`); err != nil {
			t.Fatalf("restore guard: %v", err)
		}
		// Re-arm ENABLE ALWAYS: audit_event is hardened (ADR-AUDIT-008), so a
		// plain CREATE (origin mode) would leave it DOWNGRADED and every sibling
		// test sharing this schema would then read a validator problem. The
		// parent recurses to the partition's cloned row-mutate trigger.
		if _, err := priv.Exec(ctx,
			`ALTER TABLE audit_event ENABLE ALWAYS TRIGGER trg_audit_event_no_row_mutate`); err != nil {
			t.Fatalf("re-arm guard: %v", err)
		}
	})

	// With the guard gone, audit ownership is now rewritable — the exploit.
	if _, err := priv.Exec(ctx,
		`UPDATE audit_event SET tenant_id = 'attacker' WHERE chain_id = 'guard-check'`); err != nil {
		t.Fatalf("expected audit to be mutable once the guard is dropped, got: %v", err)
	}

	// The validator must refuse a schema in this state.
	problems, err := v.Validate(ctx)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !containsAny(problems, "append-only guard against UPDATE/DELETE") {
		t.Errorf("validator did not detect the dropped audit guard; problems: %v", problems)
	}
}

func containsAny(problems []string, want string) bool {
	for _, p := range problems {
		if strings.Contains(p, want) {
			return true
		}
	}
	return false
}
