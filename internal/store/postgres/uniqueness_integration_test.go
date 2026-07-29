//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// Uniqueness must be scoped to the isolation boundary.
//
// Row-level security filters which rows a connection may SEE. It has no effect
// on whether a constraint considers them. A globally unique key on a
// tenant-scoped table is therefore a cross-tenant channel by construction: one
// tenant occupies a value and every other tenant is refused it, or has its
// write silently absorbed by ON CONFLICT.
//
// This was exploited through Idempotency-Key, the one such column whose value
// the client supplies (migration 20260730000001).
//
// serverGeneratedIdentifiers records the constraints that remain global because
// their columns are minted by the platform — ULIDs and random hex — so no
// client can choose a colliding value. Each entry is a claim that the
// identifier is NOT client-supplied. If that ever changes, the entry has to be
// removed and the constraint scoped, and this test is where that decision
// surfaces.
var serverGeneratedIdentifiers = map[string]string{
	"configuration_object_pkey":   "cfg_id is a server-minted ULID",
	"artifact_version_pkey":       "keyed on the server-minted cfg_id",
	"configuration_metadata_pkey": "keyed on the server-minted cfg_id",
	"dependency_edge_pkey":        "id is server-minted",
	"uq_edge_from_to_kind":        "keyed on server-minted cfg_ids",
	"audit_chain_head_pkey":       "chain_id is the server-minted cfg_id",
	"pk_audit_event":              "chain_id is the server-minted cfg_id",
	"uq_audit_event_id":           "event_id is derived server-side",
	"uq_audit_this_hash":          "this_hash is a SHA-256 computed server-side",
	"webhook_pkey":                "id is 'wh_' + server random hex",
	"webhook_delivery_pkey":       "id is 'dlv_' + server random hex",
	"webhook_replay_job_pkey":     "id is server-minted",
	"policy_pkey":                 "id is server-minted",
	"policy_execution_pkey":       "id is server-minted",
}

// TestUniquenessIsScopedToTenant fails when a tenant-owned table carries a
// unique constraint that neither includes tenant_id nor is justified above.
func TestUniquenessIsScopedToTenant(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx, `
		SELECT c.relname, con.conname,
		       'tenant_id' = ANY(array_agg(a.attname)) AS has_tenant
		  FROM pg_constraint con
		  JOIN pg_class c ON c.oid = con.conrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  CROSS JOIN LATERAL unnest(con.conkey) AS k(attnum)
		  JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = k.attnum
		 WHERE n.nspname = $1
		   AND con.contype IN ('p','u')
		   AND c.relname = ANY($2)
		 GROUP BY c.relname, con.conname`,
		itestSchema, tenantOwnedTables)
	if err != nil {
		t.Fatalf("enumerate constraints: %v", err)
	}
	defer rows.Close()

	checked := 0
	for rows.Next() {
		var table, constraint string
		var hasTenant bool
		if err := rows.Scan(&table, &constraint, &hasTenant); err != nil {
			t.Fatalf("scan: %v", err)
		}
		checked++
		if hasTenant {
			continue
		}
		if _, justified := serverGeneratedIdentifiers[constraint]; justified {
			continue
		}
		t.Errorf("%s.%s is unique across all tenants but does not include tenant_id. "+
			"If its columns are client-supplied this is a cross-tenant channel: one "+
			"tenant can occupy a value and deny or absorb another tenant's write. "+
			"Scope it to tenant_id, or record in serverGeneratedIdentifiers why the "+
			"value cannot be chosen by a client.", table, constraint)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	if checked == 0 {
		t.Fatal("no constraints inspected — the query is wrong, not the schema")
	}
}

// The exploit itself, as a regression: two tenants using the same
// Idempotency-Key must not interfere, in either direction.
func TestIdempotencyKeysDoNotCollideAcrossTenants(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()

	tenants := NewTenantStore(priv)
	victim, err := tenants.Create(ctx, newTenant("idem-victim", "ext-idem-victim"))
	if err != nil {
		t.Fatalf("create tenant: %v", err)
	}

	store := NewIdempotencyStore(tenantScopedPool(t))
	const sharedKey = "shared-idempotency-key"

	attackerCtx := domain.WithTenant(ctx, &domain.Tenant{
		TenantID: domain.SystemTenantID, Status: domain.TenantActive})
	victimCtx := domain.WithTenant(ctx, victim)

	if err := store.Save(attackerCtx, sharedKey, "POST", "/v1/artifacts", 201,
		[]byte(`{"cfg_id":"attacker-artifact"}`)); err != nil {
		t.Fatalf("attacker save: %v", err)
	}

	// The victim's own save must not be absorbed by the attacker's row.
	if err := store.Save(victimCtx, sharedKey, "POST", "/v1/artifacts", 201,
		[]byte(`{"cfg_id":"victim-artifact"}`)); err != nil {
		t.Fatalf("victim save: %v", err)
	}

	// The victim's retry must replay the victim's own response — this is the
	// step that previously found nothing and re-executed the write.
	status, body, found, err := store.Lookup(victimCtx, sharedKey)
	if err != nil {
		t.Fatalf("victim lookup: %v", err)
	}
	if !found {
		t.Fatal("the victim's idempotent response was lost; a retry would re-execute the write")
	}
	if status != 201 || string(body) != `{"cfg_id":"victim-artifact"}` {
		t.Fatalf("victim replayed the wrong response: status=%d body=%s", status, body)
	}

	// And the attacker still replays its own, unchanged.
	_, aBody, aFound, err := store.Lookup(attackerCtx, sharedKey)
	if err != nil || !aFound {
		t.Fatalf("attacker lookup: found=%v err=%v", aFound, err)
	}
	if string(aBody) != `{"cfg_id":"attacker-artifact"}` {
		t.Fatalf("attacker's response was overwritten: %s", aBody)
	}
}
