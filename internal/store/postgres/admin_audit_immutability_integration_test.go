//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

// seedAdminAuditRow writes one sealed-looking administrative record using the
// privileged pool. The hashes are well-formed lengths rather than real chain
// hashes: OPS-S034 delivers the schema and its guards, and the appender that
// computes them is OPS-S035. What these tests prove is that the row cannot be
// changed once written, whatever wrote it.
func seedAdminAuditRow(ctx context.Context, t *testing.T, pool *pgxpool.Pool, chainID string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO admin_audit_event
			(chain_id, seq, event_id, operation_id, operation, actor,
			 subject_org_id, subject_tenant_id, payload_canonical, payload,
			 prev_hash, this_hash, occurred_at)
		VALUES ($1, 1, $2, $3, 'user.created', 'platform-admin',
			'org-fixture', 'tenant-fixture', '{"subject_org_id":"org-fixture"}',
			'{"subject_org_id":"org-fixture"}'::jsonb, $4, $5, now())`,
		chainID, ulid.Make().String(), ulid.Make().String(),
		make([]byte, 32), append([]byte{0x01}, make([]byte, 31)...)); err != nil {
		t.Fatalf("seed admin audit row: %v", err)
	}
}

// THE ACCEPTANCE CRITERION for OPS-S034: "Triggers as for audit_event; tamper
// attempt fails." Each of the three mutation verbs must be refused by the
// database itself, not by an application check a future caller could forget.
func TestAdminAudit_IsAppendOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAdminAuditRow(ctx, t, pool, "admin-append-only-check")

	if _, err := pool.Exec(ctx, `TRUNCATE admin_audit_event`); err == nil {
		t.Error("admin_audit_event must refuse TRUNCATE")
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM admin_audit_event WHERE chain_id = 'admin-append-only-check'`); err == nil {
		t.Error("admin_audit_event must refuse DELETE")
	}
	if _, err := pool.Exec(ctx,
		`UPDATE admin_audit_event SET actor = 'tampered' WHERE chain_id = 'admin-append-only-check'`); err == nil {
		t.Error("admin_audit_event must refuse UPDATE")
	}

	// The row is still exactly as written.
	var actor string
	if err := pool.QueryRow(ctx,
		`SELECT actor FROM admin_audit_event WHERE chain_id = 'admin-append-only-check'`).Scan(&actor); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if actor != "platform-admin" {
		t.Errorf("actor = %q after three refused tamper attempts, want %q", actor, "platform-admin")
	}
}

// audit_event's guards are created in the default "origin" firing mode, so a
// session that sets session_replication_role = 'replica' suppresses them and
// rewrites history with no error raised — measured on a live database while
// this story was reviewed. The application's own credential can do this. These
// guards are ENABLE ALWAYS, which is the difference, and this test is the only
// thing standing between that decision and a silent regression to the default.
func TestAdminAudit_GuardsSurviveReplicationRoleBypass(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	seedAdminAuditRow(ctx, t, pool, "admin-replica-bypass-check")

	// One connection, so the SET and the UPDATE share a session.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SET session_replication_role = 'replica'`); err != nil {
		t.Fatalf("set replication role: %v", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SET session_replication_role = 'origin'`) }()

	if _, err := conn.Exec(ctx,
		`UPDATE admin_audit_event SET actor = 'tampered' WHERE chain_id = 'admin-replica-bypass-check'`); err == nil {
		t.Error("session_replication_role='replica' bypassed the row-level guard — the triggers are not ENABLE ALWAYS")
	}
	if _, err := conn.Exec(ctx, `TRUNCATE admin_audit_event`); err == nil {
		t.Error("session_replication_role='replica' bypassed the TRUNCATE guard — the trigger is not ENABLE ALWAYS")
	}
}

// ADR-AUDIT-007 §6.4 forbids row-level security here, and
// 20260729000001_rls_policies.sql ends with an ALTER DEFAULT PRIVILEGES that
// grants SELECT/INSERT/UPDATE/DELETE on every later table to oneops_app — the
// role every request-scoped connection assumes. Without the REVOKE in this
// story's migration, the request path would arrive holding write access to the
// administrative audit trail. Privilege is the second layer that audit_event's
// header promises and does not have.
func TestAdminAudit_RequestPathRoleHoldsNoAccess(t *testing.T) {
	pool := testPool(t) // creates the schema and applies migrations
	scoped := tenantScopedPool(t)
	ctx := context.Background()

	if _, err := scoped.Exec(ctx, `
		INSERT INTO admin_audit_event
			(chain_id, seq, event_id, operation_id, operation, actor,
			 payload_canonical, payload, prev_hash, this_hash)
		VALUES ('forged', 1, 'e', 'o', 'user.created', 'attacker',
			'{}', '{}'::jsonb, $1, $2)`,
		make([]byte, 32), append([]byte{0x02}, make([]byte, 31)...)); err == nil {
		t.Error("the request-path role could INSERT into admin_audit_event; OPS-S034 ships no writer, so nothing should hold INSERT")
	}
	if _, err := scoped.Exec(ctx, `UPDATE admin_audit_event SET actor = 'x'`); err == nil {
		t.Error("the request-path role could UPDATE admin_audit_event")
	}
	if _, err := scoped.Exec(ctx, `DELETE FROM admin_audit_event`); err == nil {
		t.Error("the request-path role could DELETE from admin_audit_event")
	}
	// §6.5 confines visibility to platform administrators. The tenant-scoped
	// role is not one, and there is no RLS policy to fall back on.
	if _, err := scoped.Exec(ctx, `SELECT 1 FROM admin_audit_event`); err == nil {
		t.Error("the request-path role could SELECT from admin_audit_event; §6.5 confines it to platform administrators")
	}
	// The chain head is not mere bookkeeping. chain_id identifies the
	// organisation, last_seq counts the administrative acts committed against
	// it, and updated_at times the most recent one. Readable on a table with no
	// row-level security, that is an enumeration oracle over every customer's
	// administrative activity.
	if _, err := scoped.Exec(ctx, `SELECT 1 FROM admin_audit_chain_head`); err == nil {
		t.Error("the request-path role could SELECT from admin_audit_chain_head — " +
			"organisation activity, act counts and timing are enumerable without row-level security")
	}

	// Belt and braces: assert the privilege state directly, so a future
	// ALTER DEFAULT PRIVILEGES or a restore that reinstates grants is caught
	// even if no query happens to exercise it.
	for _, table := range []string{"admin_audit_event", "admin_audit_chain_head"} {
		for _, priv := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
			var held bool
			if err := pool.QueryRow(ctx,
				`SELECT has_table_privilege('oneops_app', current_schema() || '.' || $1, $2)`,
				table, priv).Scan(&held); err != nil {
				t.Fatalf("check %s on %s: %v", priv, table, err)
			}
			if held {
				t.Errorf("oneops_app holds %s on %s; OPS-S034 ships no writer and no reader, "+
					"so the request-path role must hold nothing", priv, table)
			}
		}
	}
}

// The catalogue row for a trigger survives ALTER TABLE ... DISABLE TRIGGER —
// tgenabled flips to 'D' and the trigger stops firing. A validator that counted
// rows by name reported a fully disarmed table as healthy; measured on a live
// database, both counts stayed at 1 while every row was rewritable. This test
// covers the predicate that closes it.
func TestAdminAudit_DisabledGuardIsDetected(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	v := NewSchemaValidator(priv)

	if problems, err := v.Validate(ctx); err != nil {
		t.Fatalf("validate clean: %v", err)
	} else if len(problems) != 0 {
		t.Fatalf("clean schema reported problems: %v", problems)
	}

	if _, err := priv.Exec(ctx,
		`ALTER TABLE admin_audit_event DISABLE TRIGGER trg_admin_audit_event_no_row_mutate`); err != nil {
		t.Fatalf("disable guard: %v", err)
	}
	t.Cleanup(func() {
		if _, err := priv.Exec(context.Background(),
			`ALTER TABLE admin_audit_event ENABLE ALWAYS TRIGGER trg_admin_audit_event_no_row_mutate`); err != nil {
			t.Fatalf("restore guard: %v", err)
		}
	})

	// The guard is disarmed: this is the exploit the validator must report.
	seedAdminAuditRow(ctx, t, priv, "admin-disabled-guard-check")
	if _, err := priv.Exec(ctx,
		`UPDATE admin_audit_event SET actor = 'attacker' WHERE chain_id = 'admin-disabled-guard-check'`); err != nil {
		t.Fatalf("expected admin audit to be mutable once the guard is disabled, got: %v", err)
	}

	problems, err := v.Validate(ctx)
	if err != nil {
		t.Fatalf("validate tampered: %v", err)
	}
	if !anyContains(problems, "DISABLED") || !anyContains(problems, "admin_audit_event") {
		t.Errorf("a disabled append-only guard on admin_audit_event was not reported; problems = %v", problems)
	}
	// Disabled and dropped are different operator errors and must not be
	// conflated: reporting a switched-off trigger as absent sends the operator
	// looking for a missing migration.
	if anyContains(problems, "has no append-only guard against UPDATE/DELETE") {
		t.Errorf("a DISABLED guard was reported as absent; problems = %v", problems)
	}
}

// pg_trigger.tgenabled has four values and only 'D' is a switched-off trigger.
// 'R' (replica) fires ONLY under session_replication_role='replica' — that is,
// never during application traffic — so a plain UPDATE succeeds with no session
// setting at all. 'O' (origin) fires normally but is suppressed by that same
// setting, which is the bypass ENABLE ALWAYS exists to close.
//
// A predicate of "not disabled" reported both as healthy. Measured live: after
// ENABLE REPLICA the counter read 1 while an ordinary UPDATE rewrote the row.
// Each mode gets its own case here because each is a different repair.
func TestAdminAudit_DowngradedGuardModesAreDetected(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	v := NewSchemaValidator(priv)

	if problems, err := v.Validate(ctx); err != nil {
		t.Fatalf("validate clean: %v", err)
	} else if len(problems) != 0 {
		t.Fatalf("clean schema reported problems: %v", problems)
	}

	cases := []struct {
		name     string
		alter    string
		expected string
		// bypass describes how the guard is defeated once downgraded, so the
		// test proves the mode is genuinely unsafe rather than merely unusual.
		replicaModeNeeded bool
	}{
		{
			name:              "replica mode is inert during normal traffic",
			alter:             "ENABLE REPLICA TRIGGER",
			expected:          "DISABLED",
			replicaModeNeeded: false,
		},
		{
			name:              "origin mode reopens the session_replication_role bypass",
			alter:             "ENABLE TRIGGER",
			expected:          "DOWNGRADED",
			replicaModeNeeded: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := priv.Exec(ctx,
				`ALTER TABLE admin_audit_event `+tc.alter+` trg_admin_audit_event_no_row_mutate`); err != nil {
				t.Fatalf("downgrade guard: %v", err)
			}
			t.Cleanup(func() {
				if _, err := priv.Exec(context.Background(),
					`ALTER TABLE admin_audit_event ENABLE ALWAYS TRIGGER trg_admin_audit_event_no_row_mutate`); err != nil {
					t.Fatalf("re-arm guard: %v", err)
				}
			})

			// Prove the downgrade is exploitable, not merely cosmetic.
			chain := "admin-downgrade-" + tc.alter
			seedAdminAuditRow(ctx, t, priv, chain)
			conn, err := priv.Acquire(ctx)
			if err != nil {
				t.Fatalf("acquire: %v", err)
			}
			defer conn.Release()
			if tc.replicaModeNeeded {
				if _, err := conn.Exec(ctx, `SET session_replication_role = 'replica'`); err != nil {
					t.Fatalf("set replication role: %v", err)
				}
				defer func() {
					_, _ = conn.Exec(context.Background(), `SET session_replication_role = 'origin'`)
				}()
			}
			if _, err := conn.Exec(ctx,
				`UPDATE admin_audit_event SET actor = 'attacker' WHERE chain_id = $1`, chain); err != nil {
				t.Fatalf("expected the downgraded guard to permit the rewrite, got: %v", err)
			}

			problems, err := v.Validate(ctx)
			if err != nil {
				t.Fatalf("validate downgraded: %v", err)
			}
			if !anyContains(problems, tc.expected) || !anyContains(problems, "admin_audit_event") {
				t.Errorf("a %s guard was not reported as %s; problems = %v", tc.alter, tc.expected, problems)
			}
		})
	}
}

// The same subject as TestSchema_DroppedAuditGuardIsDetected, for the
// administrative store. Registering the new tables in the existing validator
// rather than writing a second one is what makes this a two-line change.
func TestAdminAudit_DroppedGuardIsDetected(t *testing.T) {
	priv := testPool(t)
	ctx := context.Background()
	v := NewSchemaValidator(priv)

	// Both guards are covered. Dropping only one leaves the other's
	// absent-versus-disabled branch unexercised, which is how a validator that
	// misreports a missing trigger as a switched-off one survives its tests.
	cases := []struct {
		trigger  string
		restore  string
		expected string
	}{
		{
			trigger: "trg_admin_audit_event_no_row_mutate",
			restore: `CREATE OR REPLACE TRIGGER trg_admin_audit_event_no_row_mutate
				BEFORE UPDATE OR DELETE ON admin_audit_event
				FOR EACH ROW EXECUTE FUNCTION admin_audit_event_immutable()`,
			expected: "has no append-only guard against UPDATE/DELETE",
		},
		{
			trigger: "trg_admin_audit_event_no_truncate",
			restore: `CREATE OR REPLACE TRIGGER trg_admin_audit_event_no_truncate
				BEFORE TRUNCATE ON admin_audit_event
				FOR EACH STATEMENT EXECUTE FUNCTION admin_audit_event_immutable()`,
			expected: "has no append-only guard against TRUNCATE",
		},
	}

	for _, tc := range cases {
		t.Run(tc.trigger, func(t *testing.T) {
			if _, err := priv.Exec(ctx,
				`DROP TRIGGER `+tc.trigger+` ON admin_audit_event`); err != nil {
				t.Fatalf("drop guard: %v", err)
			}
			t.Cleanup(func() {
				if _, err := priv.Exec(context.Background(), tc.restore); err != nil {
					t.Fatalf("restore guard: %v", err)
				}
				if _, err := priv.Exec(context.Background(),
					`ALTER TABLE admin_audit_event ENABLE ALWAYS TRIGGER `+tc.trigger); err != nil {
					t.Fatalf("re-arm guard: %v", err)
				}
			})

			problems, err := v.Validate(ctx)
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			if !anyContains(problems, tc.expected) || !anyContains(problems, "admin_audit_event") {
				t.Errorf("a dropped %s was not reported as absent; problems = %v", tc.trigger, problems)
			}
			// The converse of the assertion in
			// TestAdminAudit_DisabledGuardIsDetected: a trigger that is gone
			// from the catalogue is absent, not disabled.
			if anyContains(problems, "DISABLED") {
				t.Errorf("a DROPPED guard was reported as disabled; problems = %v", problems)
			}
		})
	}
}

// The constraints exist so that a row which never passed through the appender
// cannot masquerade as sealed history. Each case below is a distinct forgery.
func TestAdminAudit_SchemaRefusesUnsealedRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	insert := func(t *testing.T, seq int, operation, actor string, payload []byte, prev, this []byte) error {
		t.Helper()
		_, err := pool.Exec(ctx, `
			INSERT INTO admin_audit_event
				(chain_id, seq, event_id, operation_id, operation, actor,
				 payload_canonical, payload, prev_hash, this_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7, '{}'::jsonb, $8, $9)`,
			"unsealed-"+ulid.Make().String(), seq, ulid.Make().String(),
			ulid.Make().String(), operation, actor, payload, prev, this)
		return err
	}

	good := func() ([]byte, []byte, []byte) {
		return []byte("{}"), make([]byte, 32), append([]byte{0x03}, make([]byte, 31)...)
	}

	t.Run("seq below one is refused", func(t *testing.T) {
		p, prev, this := good()
		if err := insert(t, 0, "user.created", "admin", p, prev, this); err == nil {
			t.Error("seq = 0 was accepted; a direct INSERT could wedge a chain against its own primary key")
		}
	})
	t.Run("empty payload is refused", func(t *testing.T) {
		_, prev, this := good()
		if err := insert(t, 1, "user.created", "admin", []byte{}, prev, this); err == nil {
			t.Error("an empty payload_canonical was accepted; audit.ChainHash refuses one, so the database must too")
		}
	})
	t.Run("empty actor is refused", func(t *testing.T) {
		p, prev, this := good()
		if err := insert(t, 1, "user.created", "", p, prev, this); err == nil {
			t.Error("an empty actor was accepted; an unattributable administrative record is not a record")
		}
	})
	t.Run("short prev_hash is refused", func(t *testing.T) {
		p, _, this := good()
		if err := insert(t, 1, "user.created", "admin", p, make([]byte, 31), this); err == nil {
			t.Error("a 31-byte prev_hash was accepted; the chain link must be a full SHA-256")
		}
	})
	t.Run("a governance operation is refused", func(t *testing.T) {
		p, prev, this := good()
		// 'ratification' is one of audit_event's twelve values. Accepting it
		// here would put one fact in two stores and break the disjointness the
		// constitutional approval is conditioned on.
		if err := insert(t, 1, "ratification", "admin", p, prev, this); err == nil {
			t.Error("a governance operation was accepted into the administrative store; the vocabularies must stay disjoint")
		}
	})
	t.Run("an administrative operation is accepted", func(t *testing.T) {
		p, prev, this := good()
		if err := insert(t, 1, "membership.granted", "admin", p, prev, this); err != nil {
			t.Errorf("a valid administrative operation was refused: %v", err)
		}
	})
}

// ADR-AUDIT-007 §6.3/§6.4: the platform owns every row, the store is global,
// and the subject is an attribute. This asserts the three structural facts that
// make that true, so a later migration cannot quietly convert the store into a
// tenant-owned table and inherit delivery and read paths with it.
func TestAdminAudit_IsGlobalAndPlatformOwned(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	for _, table := range []string{"admin_audit_event", "admin_audit_chain_head"} {
		var hasTenantID bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_schema = current_schema()
				   AND table_name = $1
				   AND column_name = 'tenant_id')`, table).Scan(&hasTenantID); err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if hasTenantID {
			t.Errorf("%s carries a tenant_id column; §6.4 requires the subject to be an attribute, "+
				"and the name tenant_id is reserved for ownership keys", table)
		}

		var rls, forced bool
		if err := pool.QueryRow(ctx, `
			SELECT c.relrowsecurity, c.relforcerowsecurity
			  FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = current_schema() AND c.relname = $1`, table).Scan(&rls, &forced); err != nil {
			t.Fatalf("inspect rls on %s: %v", table, err)
		}
		if rls || forced {
			t.Errorf("%s has row-level security enabled; §6.4 makes this store global, and isolation "+
				"here is the table it lives in plus requirePlatformAdmin, not a predicate", table)
		}

		for _, owned := range TenantOwnedTables {
			if owned == table {
				t.Errorf("%s is in TenantOwnedTables; §6.11 forbids adding it, and doing so would make "+
					"the startup validators demand an ownership key it must not have", table)
			}
		}
	}

	// The subject columns exist, under names that are attributes.
	for _, col := range []string{"subject_org_id", "subject_tenant_id"} {
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				 WHERE table_schema = current_schema()
				   AND table_name = 'admin_audit_event' AND column_name = $1)`, col).Scan(&exists); err != nil {
			t.Fatalf("inspect column %s: %v", col, err)
		}
		if !exists {
			t.Errorf("admin_audit_event has no %s column; §6.1 requires the record to say to whom", col)
		}
	}
}

// §6.5: "It is not an event source." The relay, replay and the policy consumer
// all enumerate chains through AuditStore.ListChainIDs, which reads
// audit_chain_head. Because the administrative chains live in their own head
// table, they are undeliverable by construction rather than by filter — this
// test is what keeps that structural, so a later change that pointed the
// administrative appender at audit_chain_head would be caught here rather than
// discovered in a subscriber's inbox.
func TestAdminAudit_IsNotAnEventSource(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO admin_audit_chain_head (chain_id, last_seq, last_hash)
		VALUES ('admin-chain-not-an-event-source', 1, $1)`,
		append([]byte{0x04}, make([]byte, 31)...)); err != nil {
		t.Fatalf("seed admin chain head: %v", err)
	}

	chains, err := NewAuditStore(pool).ListChainIDs(ctx)
	if err != nil {
		t.Fatalf("list chain ids: %v", err)
	}
	for _, c := range chains {
		if c == "admin-chain-not-an-event-source" {
			t.Fatal("an administrative chain is visible to ListChainIDs — the relay, replay and policy " +
				"consumer would all walk it, and §6.5's undeliverability would become a filter rather " +
				"than a property of where the rows live")
		}
	}
}

func anyContains(problems []string, substr string) bool {
	for _, p := range problems {
		if strings.Contains(p, substr) {
			return true
		}
	}
	return false
}
