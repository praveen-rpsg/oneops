package postgres

// TenantOwnedTables is the canonical set of tables that hold tenant data and are
// protected by row-level security and a mandatory tenant_id (ADR-TENANCY-001).
// It is the single source of truth for the startup validators; a table added to
// the schema with a tenant_id but omitted here would escape both the RLS check
// and the referential check, so an integration test cross-checks this list
// against the live schema and fails the build on drift.
//
// `tenant` itself is excluded by design: resolving a token to a tenant precedes
// binding one, so the registry cannot be tenant-scoped (ADR-TENANCY-001 §4).
// Worker cursors (webhook_cursor, policy_cursor) are excluded because they hold
// platform processing state, not customer data, and carry no tenant_id.
var TenantOwnedTables = []string{
	"configuration_object",
	"configuration_metadata",
	"artifact_version",
	"dependency_edge",
	"idempotency_key",
	"audit_event",
	"audit_chain_head",
	"webhook",
	"webhook_delivery",
	"webhook_replay_job",
	"policy",
	"policy_execution",
}
