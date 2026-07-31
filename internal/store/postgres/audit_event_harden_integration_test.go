//go:build integration

package postgres

import (
	"context"
	"testing"
)

// audit_event's append-only guards were created in the default "origin" firing
// mode, so a session that set session_replication_role = 'replica' suppressed
// them and rewrote or erased history with no error — proven live before
// ADR-AUDIT-008. 20260809000001 arms both guards ENABLE ALWAYS on the parent and
// on every partition. This test is the direct re-run of that exploit and the
// only thing standing between the hardening and a silent regression to origin
// mode. Reverting the migration makes both mutations succeed and this fails.
func TestAuditEvent_GuardsSurviveReplicationRoleBypass(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()
	seedAuditRow(ctx, t, priv, "audit-replica-bypass-check")

	// One connection, so SET and the mutation share a session. The privileged
	// pool assumes the owning role, which in the dev stack is the cluster
	// superuser — the only role that can set session_replication_role at all
	// (oneops_app gets "permission denied to set parameter"). That is the exact
	// insider/owning-pool threat this hardening addresses.
	conn, err := priv.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SET session_replication_role = 'replica'`); err != nil {
		t.Fatalf("set replication role: %v", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SET session_replication_role = 'origin'`) }()

	if _, err := conn.Exec(ctx,
		`UPDATE audit_event SET actor = 'tampered' WHERE chain_id = 'audit-replica-bypass-check'`); err == nil {
		t.Error("session_replication_role='replica' bypassed the row-level guard — the triggers are not ENABLE ALWAYS")
	}
	// The partition by name: PostgreSQL does not propagate the statement-level
	// TRUNCATE trigger to partitions, so this is the specific hole 20260728000002
	// closed and 20260809000001 must keep closed under replica mode.
	if _, err := conn.Exec(ctx, `TRUNCATE audit_event_default`); err == nil {
		t.Error("session_replication_role='replica' bypassed the TRUNCATE guard on the partition — it is not ENABLE ALWAYS")
	}

	// The row is untouched: nothing was rewritten and nothing was erased.
	var actor string
	if err := priv.QueryRow(ctx,
		`SELECT actor FROM audit_event WHERE chain_id = 'audit-replica-bypass-check'`).Scan(&actor); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if actor != "test" {
		t.Errorf("actor = %q after a refused tamper attempt, want %q", actor, "test")
	}
}

// Layer two: an append-only table must hold no write privilege beyond INSERT.
// 20260729000001 grants SELECT/INSERT/UPDATE/DELETE on every table to
// oneops_app; without the REVOKE in 20260809000001 the request-path role arrives
// holding UPDATE and DELETE on the constitutional audit chain. SELECT and INSERT
// are retained — the request path appends events and the verifier reads them.
// The grant set is asserted directly, on the parent and the partition, so a
// widening (or a re-grant by a rolled-back migration) is caught even when no
// query happens to exercise it.
func TestAuditEvent_RequestPathRoleHoldsOnlyAppendAndRead(t *testing.T) {
	pool := testPool(t)
	ctx := adminTestCtx()

	want := map[string]map[string]bool{
		"audit_event":         {"SELECT": true, "INSERT": true, "UPDATE": false, "DELETE": false, "TRUNCATE": false},
		"audit_event_default": {"SELECT": true, "INSERT": true, "UPDATE": false, "DELETE": false, "TRUNCATE": false},
	}
	for table, privs := range want {
		for priv, expected := range privs {
			var held bool
			if err := pool.QueryRow(ctx,
				`SELECT has_table_privilege('oneops_app', current_schema() || '.' || $1, $2)`,
				table, priv).Scan(&held); err != nil {
				t.Fatalf("check %s on %s: %v", priv, table, err)
			}
			if held != expected {
				t.Errorf("oneops_app %s on %s = %v, want %v", priv, table, held, expected)
			}
		}
	}
}

// The boot validator's alwaysRequired flag flips to true for audit_event with
// this story. A downgrade of a hardened guard back to origin mode re-opens the
// replica-role bypass, so the validator must report it DOWNGRADED (distinct from
// DISABLED and from absent). This proves the flip bites: with alwaysRequired
// false the origin-mode guard reads as healthy and this test fails.
func TestAuditEvent_DowngradedGuardIsDetected(t *testing.T) {
	priv := testPool(t)
	ctx := adminTestCtx()
	v := NewSchemaValidator(priv)

	if problems, err := v.Validate(ctx); err != nil {
		t.Fatalf("validate clean: %v", err)
	} else if len(problems) != 0 {
		t.Fatalf("clean schema reported problems: %v", problems)
	}

	// Downgrade the parent's row-mutate guard to origin mode. The parent recurses
	// to the partition's cloned trigger, so this reopens the bypass everywhere.
	if _, err := priv.Exec(ctx,
		`ALTER TABLE audit_event ENABLE TRIGGER trg_audit_event_no_row_mutate`); err != nil {
		t.Fatalf("downgrade guard: %v", err)
	}
	t.Cleanup(func() {
		if _, err := priv.Exec(context.Background(),
			`ALTER TABLE audit_event ENABLE ALWAYS TRIGGER trg_audit_event_no_row_mutate`); err != nil {
			t.Fatalf("re-arm guard: %v", err)
		}
	})

	// Prove the downgrade is genuinely exploitable, not merely cosmetic.
	seedAuditRow(ctx, t, priv, "audit-downgrade-check")
	conn, err := priv.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET session_replication_role = 'replica'`); err != nil {
		t.Fatalf("set replication role: %v", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SET session_replication_role = 'origin'`) }()
	if _, err := conn.Exec(ctx,
		`UPDATE audit_event SET actor = 'attacker' WHERE chain_id = 'audit-downgrade-check'`); err != nil {
		t.Fatalf("expected the downgraded guard to permit the rewrite, got: %v", err)
	}

	problems, err := v.Validate(ctx)
	if err != nil {
		t.Fatalf("validate downgraded: %v", err)
	}
	if !anyContains(problems, "DOWNGRADED") || !anyContains(problems, "audit_event") {
		t.Errorf("a downgraded audit_event guard was not reported as DOWNGRADED; problems = %v", problems)
	}
}
