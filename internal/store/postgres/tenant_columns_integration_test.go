//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/oklog/ulid/v2"

	"github.com/rpsg/oneops/internal/domain"
)

// tenantOwnedTables aliases the canonical production list so these tests and the
// startup validators cannot drift apart.
var tenantOwnedTables = TenantOwnedTables

func TestTenantColumns_EveryOwnedTableCarriesTenant(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	for _, table := range tenantOwnedTables {
		var nullable string
		err := pool.QueryRow(ctx, `
			SELECT is_nullable
			  FROM information_schema.columns
			 WHERE table_schema = $1 AND table_name = $2 AND column_name = 'tenant_id'`,
			itestSchema, table).Scan(&nullable)
		if err != nil {
			t.Errorf("%s has no tenant_id column: %v", table, err)
			continue
		}
		// A nullable tenant_id would let a row be unowned, and an unowned row
		// disappears from every tenant-scoped query rather than raising.
		if nullable != "NO" {
			t.Errorf("%s.tenant_id is nullable; ownership must be total", table)
		}
	}
}

// Existing rows are backfilled to the system tenant rather than left unowned,
// which is what makes the migration safe to apply to a live database.
func TestTenantColumns_BackfillsToSystem(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	repo := NewConfigObjectRepo(pool)
	created, err := repo.Create(ctx, sample("backfill-check.md", "1.0.0"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	var tenantID string
	if err := pool.QueryRow(ctx,
		`SELECT tenant_id FROM configuration_object WHERE cfg_id = $1`, created.CfgID,
	).Scan(&tenantID); err != nil {
		t.Fatalf("read tenant_id: %v", err)
	}
	if tenantID != domain.SystemTenantID {
		t.Errorf("tenant_id = %q, want %q", tenantID, domain.SystemTenantID)
	}
}

// The foreign key is the substance of the migration: a row cannot be owned by
// a tenant that does not exist.
func TestTenantColumns_ForeignKeyRejectsUnknownTenant(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	_, err := pool.Exec(ctx, `
		INSERT INTO configuration_object
			(cfg_id, artifact, version, role, lifecycle, retention_class, tenant_id)
		VALUES ($1, 'orphan.md', '1.0.0', 'reference', 'draft', 'working_material', 'no-such-tenant')`,
		ulid.Make().String())
	if err == nil {
		t.Fatal("a row owned by an unregistered tenant must be rejected")
	}
}

// Uniqueness is per tenant, not global. Two tenants must be able to hold an
// artifact of the same name and version; a global constraint would leak one
// tenant's naming into another's namespace and give them a way to detect each
// other through conflicts.
func TestTenantColumns_ArtifactUniquenessIsPerTenant(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	other := NewTenantStore(pool)
	tn, err := other.Create(ctx, newTenant("uniq-tenant", "ext-uniq-tenant"))
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	insert := func(tenantID string) error {
		_, err := pool.Exec(ctx, `
			INSERT INTO configuration_object
				(cfg_id, artifact, version, role, lifecycle, retention_class, tenant_id)
			VALUES ($1, 'shared-name.md', '1.0.0', 'reference', 'draft', 'working_material', $2)`,
			ulid.Make().String(), tenantID)
		return err
	}

	if err := insert(domain.SystemTenantID); err != nil {
		t.Fatalf("system tenant insert: %v", err)
	}
	if err := insert(tn.TenantID); err != nil {
		t.Fatalf("second tenant must be allowed the same artifact/version: %v", err)
	}
	// Within one tenant the constraint still holds.
	if err := insert(tn.TenantID); err == nil {
		t.Fatal("duplicate artifact/version within a tenant must still conflict")
	}
}

// A tenant that owns rows cannot be deleted. Tenants are suspended rather than
// removed precisely because audit history is append-only and would otherwise be
// orphaned; the foreign key enforces that at the database level.
func TestTenantColumns_TenantWithRowsCannotBeDeleted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	store := NewTenantStore(pool)
	tn, err := store.Create(ctx, newTenant("undeletable", "ext-undeletable"))
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO configuration_object
			(cfg_id, artifact, version, role, lifecycle, retention_class, tenant_id)
		VALUES ($1, 'owned.md', '1.0.0', 'reference', 'draft', 'working_material', $2)`,
		ulid.Make().String(), tn.TenantID); err != nil {
		t.Fatalf("insert owned row: %v", err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM tenant WHERE tenant_id = $1`, tn.TenantID); err == nil {
		t.Fatal("deleting a tenant that owns rows must be refused")
	}
}

// seedAuditRow inserts one audit event directly, so the append-only guards can
// be exercised against a table that actually holds a row. The row-level
// triggers are FOR EACH ROW: against an empty table they never fire, and a
// DELETE that matches nothing succeeds — which is how the partition bypass
// below went unnoticed.
func seedAuditRow(ctx context.Context, t *testing.T, pool interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, chainID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO audit_event
			(chain_id, seq, event_id, operation_id, operation, actor,
			 payload_canonical, payload, prev_hash, this_hash, occurred_at)
		VALUES ($1, 1, $2, $3, 'ratification', 'test',
			'{}', '{}'::jsonb, $4, $5, now())`,
		chainID, ulid.Make().String(), ulid.Make().String(),
		make([]byte, 32), append([]byte{0x01}, make([]byte, 31)...)); err != nil {
		t.Fatalf("seed audit row: %v", err)
	}
}

// The append-only guarantee on audit history is independent of tenancy and must
// survive the column being added.
func TestTenantColumns_AuditRemainsAppendOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAuditRow(ctx, t, pool, "append-only-check")

	if _, err := pool.Exec(ctx, `TRUNCATE audit_event`); err == nil {
		t.Error("audit_event must refuse TRUNCATE after gaining tenant_id")
	}
	if _, err := pool.Exec(ctx, `DELETE FROM audit_event WHERE chain_id = 'append-only-check'`); err == nil {
		t.Error("audit_event must refuse DELETE after gaining tenant_id")
	}
	if _, err := pool.Exec(ctx,
		`UPDATE audit_event SET actor = 'tampered' WHERE chain_id = 'append-only-check'`); err == nil {
		t.Error("audit_event must refuse UPDATE after gaining tenant_id")
	}
}

// State Model §8 forbids deletion of audit history. PostgreSQL propagates
// row-level triggers from a partitioned parent to its partitions, but NOT
// statement-level TRUNCATE triggers — so `TRUNCATE audit_event` was refused
// while `TRUNCATE audit_event_default` erased the same rows. Migration
// 20260728000002 attaches the guard to every partition and installs an event
// trigger so partitions created later inherit it.
func TestAuditPartitionsCannotBeTruncatedDirectly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAuditRow(ctx, t, pool, "partition-bypass-check")

	rows, err := pool.Query(ctx, `
		SELECT inhrelid::regclass::text
		  FROM pg_inherits
		 WHERE inhparent = 'audit_event'::regclass`)
	if err != nil {
		t.Fatalf("list partitions: %v", err)
	}
	var partitions []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan: %v", err)
		}
		partitions = append(partitions, name)
	}
	rows.Close()

	if len(partitions) == 0 {
		t.Fatal("expected at least the default partition")
	}
	for _, p := range partitions {
		if _, err := pool.Exec(ctx, "TRUNCATE "+p); err == nil {
			t.Errorf("partition %s could be truncated — audit history is erasable", p)
		}
	}
}
