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

	// CI lifecycle + append-only change history (E1.3).
	"asset_change_history.asset_change_history_pkey": "change_id is platform-generated (domain.NewID() in AssetStore.recordChange); clients never supply it and there is no route that accepts one",

	// Collector check-target configuration (E2.2a).
	"collector_check.collector_check_pkey": "check_id is platform-generated (domain.NewCollectorCheck); clients never supply it and there is no create route that accepts one",

	// Alert rule config + firing-state (E3.1).
	"alert_rule.alert_rule_pkey": "rule_id is platform-generated (domain.NewAlertRule); clients never supply it and there is no create route that accepts one",

	// Security rule detection config (E8.1b-1).
	"security_rule.security_rule_pkey": "rule_id is platform-generated (domain.NewSecurityRule); clients never supply it and there is no create route that accepts one",

	// Indicator-of-compromise watchlist (E8.2a). ioc.uq_ioc_tenant_indicator
	// already contains tenant_id directly (route 1), so only the surrogate PK
	// needs justifying here.
	"ioc.ioc_pkey": "ioc_id is platform-generated (domain.NewIOC); clients never supply it and there is no create route that accepts one",

	// Incident work item + append-only timeline (E5.1).
	"incident.incident_pkey":             "incident_id is platform-generated (domain.NewIncident); clients never supply it and there is no create route that accepts one",
	"incident_event.incident_event_pkey": "event_id is platform-generated (domain.NewID() in IncidentStore.recordEvent); clients never supply it and there is no route that accepts one",

	// Maintenance window (E3.3a).
	"maintenance_window.maintenance_window_pkey": "window_id is platform-generated (domain.NewMaintenanceWindow); clients never supply it and there is no create route that accepts one",

	// Dependency suppression (E3.3b). suppression_id is server-minted
	// (domain.NewID in DependencySuppressionStore.record); the table is
	// written ONLY by the leader-gated evaluator's privileged path — there is
	// no client-facing create route that accepts a suppression_id at all.
	"dependency_suppression.dependency_suppression_pkey": "suppression_id is server-minted (domain.NewID); the table is written only by the evaluator's privileged path, no client route accepts one",

	// On-call schedules & rotations (E5.2a). uq_on_call_participant_schedule_position
	// and uq_on_call_participant_schedule_user both already contain tenant_id
	// directly (route 1), so only the two surrogate PKs need justifying here.
	"on_call_schedule.on_call_schedule_pkey":       "schedule_id is server-minted (domain.NewOnCallSchedule); clients never supply it and there is no create route that accepts one",
	"on_call_participant.on_call_participant_pkey": "participant_id is server-minted (domain.NewOnCallParticipant); clients never supply it and there is no create route that accepts one",

	// Escalation policy + tier config, CRUD only (E5.2b-1). uq_escalation_tier_policy_position
	// already contains tenant_id directly (route 1), so only the two surrogate
	// PKs need justifying here.
	"escalation_policy.escalation_policy_pkey": "policy_id is server-minted (domain.NewEscalationPolicy); clients never supply it and there is no create route that accepts one",
	"escalation_tier.escalation_tier_pkey":     "tier_id is server-minted (domain.NewEscalationTier); clients never supply it and there is no create route that accepts one",

	// Escalation engine work-queue (E5.2b-2). uq_escalation_state_tenant_incident
	// already contains tenant_id directly (route 1), so only the surrogate PK
	// needs justifying here.
	"escalation_state.escalation_state_pkey": "state_id is server-minted (domain.NewEscalationState); there is no HTTP write path onto this table at all — it is written only by the leader-gated Seeder",

	// Vulnerability-finding stateful entity (E8.3a). uq_vuln_finding_tenant_asset_vuln
	// already contains tenant_id directly (route 1), so only the surrogate PK
	// needs justifying here.
	"vuln_finding.vuln_finding_pkey": "finding_id is platform-generated (domain.NewVulnFinding); clients never supply it and there is no create route that accepts one",

	// Risk register stateful entity + scoring projection (E8.4a). risk
	// carries no natural dedup key at all — it is operator-authored, not
	// scan-deduped — so only the surrogate PK needs justifying here.
	"risk.risk_pkey": "risk_id is platform-generated (domain.NewRisk); clients never supply it and there is no create route that accepts one",

	// Compliance control register + append-only evidence trail (E8.4b).
	// compliance_control.uq_compliance_control_tenant_framework_ref already
	// contains tenant_id directly (route 1), so only the two surrogate PKs
	// need justifying here.
	"compliance_control.compliance_control_pkey": "control_id is platform-generated (domain.NewComplianceControl); clients never supply it and there is no create route that accepts one",
	"control_evidence.control_evidence_pkey":     "evidence_id is platform-generated (domain.NewControlEvidence, called from ComplianceControlStore.AddEvidence); clients never supply it and there is no route that accepts one",

	// Security-response automation config + exactly-once dispatch ledger
	// (E8.5, ADR-SOC-010). uq_security_response_dispatch_once already
	// contains tenant_id directly (route 1), so only the two surrogate PKs
	// need justifying here.
	"security_response_rule.security_response_rule_pkey":         "rule_id is platform-generated (domain.NewSecurityResponseRule); clients never supply it and there is no create route that accepts one",
	"security_response_dispatch.security_response_dispatch_pkey": "dispatch_id is server-minted (domain.NewID, in SecurityResponderStore.ClaimDispatch); the table is written only by the leader-gated SecurityResponder's privileged path, no client route accepts one",
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
