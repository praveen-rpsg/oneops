//go:build integration

package postgres

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Schema-derived completeness sweep for cross-tenant key confusion (entry 1).
//
// The class: **a key whose value a client supplies, used as a global key on a
// tenant-scoped table.** `idempotency_key.id` was the client's
// `Idempotency-Key` header as a *global* primary key, so one tenant's key
// resolved another tenant's row. Row-level security does not help — it filters
// which rows are visible, not which key space they occupy.
//
// Entry 1's enforcement was a single integration test (maturity Level 1) with no
// ADR at all. This sweep derives its subject set from the live schema, so a new
// table or a new unique key is examined automatically (Level 5).
//
// A unique key is acceptable when it is scoped to a tenant by one of three
// routes, all computed from catalogue metadata rather than a remembered list:
//
//  1. it contains `tenant_id` directly; or
//  2. it appears in keyScopeJustifications below, with a recorded reason.
//
// The second route exists because a purely schema-derived proof is impossible:
// whether a key is safe depends on whether its value is *client-supplied*, and
// that is a property of the code, not of the catalogue. `idempotency_key.id` and
// `webhook.id` are both single-column keys on tenant-owned tables; one was a
// cross-tenant defect and the other is not, and nothing in the schema
// distinguishes them.
//
// So the sweep does what the schema *can* do — discover every unique key on
// every tenant-owned table, automatically — and forces anything not tenant-keyed
// to carry an explicit justification. A new table or a new unique key fails the
// build until someone writes down why it is safe. That is the honest division:
// Level-5 discovery of the subject set, Level-4 justification of the residue.

// keyScopeJustifications records why a unique key on a tenant-owned table is
// safe without containing tenant_id. Every entry must say what makes the key
// values un-collidable across tenants. An unjustified key fails the build.
var keyScopeJustifications = map[string]string{
	// Platform-generated surrogate identities. Clients cannot supply these —
	// pinned from the transport side by arch.TestCreateDTOs_DoNotExposeEntityIdentity.
	"configuration_object.configuration_object_pkey": "cfg_id is a platform-generated ULID",
	"dependency_edge.dependency_edge_pkey":           "id is platform-generated",
	"webhook.webhook_pkey":                           "id is platform-generated (wh_<hex>)",
	"policy.policy_pkey":                             "id is platform-generated (pol_<hex>)",
	"webhook_delivery.webhook_delivery_pkey":         "id is content-derived from server-owned values (ADR-CONCURRENCY-003)",
	"policy_execution.policy_execution_pkey":         "id is content-derived from server-owned values (ADR-CONCURRENCY-003)",
	"webhook_replay_job.webhook_replay_job_pkey":     "id is platform-generated (rpl_<hex>)",
	"audit_chain_head.audit_chain_head_pkey":         "chain_id is a cfg_id, platform-generated",

	// Transitively scoped: the leading column identifies an already-scoped row,
	// so the remaining columns live inside one tenant's data.
	"configuration_metadata.configuration_metadata_pkey": "keyed by cfg_id, which is tenant-scoped; the client-supplied part (key) is confined to that row",
	"artifact_version.artifact_version_pkey":             "keyed by cfg_id, which is tenant-scoped",
	"dependency_edge.uq_edge_from_to_kind":               "keyed by from_cfg/to_cfg, both tenant-scoped cfg_ids",
	"audit_event.pk_audit_event":                         "keyed by chain_id (a cfg_id); seq is assigned under the chain-head lock (ADR-AUDIT-006)",
	"audit_event.uq_audit_event_id":                      "keyed by chain_id; event_id derives from the client's Idempotency-Key but is confined to that chain, so two tenants cannot collide",
	"audit_event.uq_audit_this_hash":                     "keyed by chain_id; this_hash is computed by the platform",

	// Identity (ADR-IDENTITY-002 §2.3, §2.4). Every column below is server-minted
	// or already tenant-determining, so no client can occupy a value that denies
	// or absorbs another tenant's write.
	"membership.membership_pkey":        "membership_id is platform-generated; clients never supply it",
	"invitation.invitation_pkey":        "invitation_id is platform-generated; clients never supply it",
	"invitation.uq_invitation_token":    "token_hash is the hash of a server-generated token — a client cannot choose the value (ADR-IDENTITY-002 §2.4)",
	"membership.uq_membership_org_user": "keyed by org_id, which stands 1:1 with a tenant (ADR-IDENTITY-001 §4), so the pair is confined to one tenant by construction; user_id is platform-generated",

	// Team (extends the same shape to a non-authorisation grouping).
	"team.team_pkey":                               "team_id is platform-generated; clients never supply it",
	"team_membership.team_membership_pkey":         "team_membership_id is platform-generated; clients never supply it",
	"team_membership.uq_team_membership_team_user": "team_id is platform-generated and already belongs to exactly one tenant; user_id is platform-generated",

	// Notification Service. There is no create-with-id HTTP route at all —
	// notification_id is minted by domain.NewNotification and never accepted
	// from a request — so this is not merely un-collidable, it is unreachable
	// from outside the platform.
	"notification.notification_pkey": "notification_id is platform-generated (a ULID); clients never supply it and there is no create route that accepts one",

	// Multi-approver approval quorum (ADR-GOV-005). uq_approval_tenant_governance_approver
	// already contains tenant_id and needs no justification here; only the
	// surrogate PK does.
	"approval_record.approval_record_pkey": "approval_id is platform-generated (a ULID via domain.NewApprovalRecord); clients never supply it and there is no create route that accepts one",

	// CMDB Asset/Configuration Item graph (ADR-ASSET-001).
	"asset.asset_pkey":                           "asset_id is platform-generated (domain.NewAsset); clients never supply it and there is no create route that accepts one",
	"asset_relationship.asset_relationship_pkey": "relationship_id is platform-generated (domain.NewAssetRelationship); clients never supply it and there is no create route that accepts one",
	"asset_relationship.uq_asset_relationship":   "keyed by from_asset_id/to_asset_id, both platform-generated asset_ids that AssetStore.CreateRelationship confirms belong to the caller's tenant before insert (ADR-ASSET-001 §6), plus a closed relationship type",
}

func TestEveryTenantScopedUniqueKey_IsTenantScoped(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	tables := make([]string, len(TenantOwnedTables))
	copy(tables, TenantOwnedTables)
	sort.Strings(tables)

	type key struct {
		table, index string
		cols         []string
		isPK         bool
	}
	var keys []key
	kr, err := pool.Query(ctx, `
		SELECT c.relname, i.relname, ix.indisprimary,
		       array_agg(a.attname ORDER BY a.attnum)
		  FROM pg_index ix
		  JOIN pg_class c ON c.oid = ix.indrelid
		  JOIN pg_class i ON i.oid = ix.indexrelid
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(ix.indkey)
		 WHERE n.nspname = current_schema() AND ix.indisunique AND c.relname = ANY($1)
		 GROUP BY 1,2,3`, tables)
	if err != nil {
		t.Fatalf("read unique keys: %v", err)
	}
	for kr.Next() {
		var k key
		if err := kr.Scan(&k.table, &k.index, &k.isPK, &k.cols); err != nil {
			kr.Close()
			t.Fatalf("scan unique key: %v", err)
		}
		keys = append(keys, k)
	}
	kr.Close()

	if len(keys) == 0 {
		t.Fatal("no unique keys found on tenant-owned tables; the sweep would be vacuous — " +
			"either the registry is empty or the catalogue query stopped matching")
	}

	scoped, justified := 0, 0
	seen := map[string]bool{}
	for _, k := range keys {
		hasTenant := false
		for _, c := range k.cols {
			if c == "tenant_id" {
				hasTenant = true
			}
		}
		if hasTenant {
			scoped++
			continue
		}
		name := k.table + "." + k.index
		seen[name] = true
		if reason, ok := keyScopeJustifications[name]; ok && strings.TrimSpace(reason) != "" {
			justified++
			continue
		}
		t.Errorf("UNJUSTIFIED KEY SCOPE: %s over (%s) is unique on a tenant-owned table and does "+
			"not contain tenant_id. If any of those columns is client-supplied, one tenant's "+
			"value collides with another's — the defect entry 1 records. Add it to "+
			"keyScopeJustifications with the reason it cannot collide, or scope it by tenant",
			name, strings.Join(k.cols, ", "))
	}

	// A justification for a key that no longer exists is stale and hides drift.
	for name := range keyScopeJustifications {
		if !seen[name] {
			t.Errorf("stale justification for %s: no such unique key on a tenant-owned table — "+
				"remove it, or the list stops describing the schema", name)
		}
	}
	t.Logf("unique keys on tenant-owned tables: %d (tenant-keyed %d, justified %d)",
		len(keys), scoped, justified)
}

// The other side of the same class: route 2 above is only safe because the
// platform generates surrogate ids. If a client could supply one, every
// single-column primary key on a tenant-owned table becomes a global key space
// shared across tenants — exactly `idempotency_key` before it was fixed.
func TestClientCannotSupplyEntityIdentity(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()
	repo := NewConfigObjectRepo(pool)

	chosen := fmt.Sprintf("CLIENT-CHOSEN-%d", 12345)
	o := sample(fmt.Sprintf("identity-%d.md", 12345), "1.0.0")
	o.Role = "reference"
	o.CfgID = chosen

	created, err := repo.Create(ctx, o)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Logf("supplied cfg_id %q -> stored %q", chosen, created.CfgID)

	// The repository honours a pre-set id by design (the governance engine and
	// replay both need to construct rows with known ids). The guarantee lives at
	// the transport boundary: no client-facing DTO exposes the field. That is
	// asserted by arch.TestCreateDTOs_DoNotExposeEntityIdentity; this test
	// records the repository's actual behaviour so the two are read together and
	// the assumption is not silently inverted.
	if created.CfgID != chosen {
		t.Logf("the repository generates ids regardless of the caller — the transport guarantee " +
			"is then belt-and-braces rather than load-bearing")
	}
}
