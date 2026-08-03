package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/store/migrate"
)

// SchemaValidator checks that the database schema still enforces the properties
// the ownership model depends on. Ownership resolution and isolation are only as
// strong as the schema underneath them: row-level security must be on, the
// ownership columns must be mandatory, and the migrations the binary was built
// for must all be applied. A migration — or an operator — can weaken any of
// these, and nothing else at runtime would notice (ADR-TENANCY-007).
type SchemaValidator struct {
	pool *pgxpool.Pool
}

// NewSchemaValidator builds a validator over the privileged pool.
func NewSchemaValidator(pool *pgxpool.Pool) *SchemaValidator {
	return &SchemaValidator{pool: pool}
}

// Validate returns one problem per broken schema invariant. An empty slice means
// the schema still enforces the ownership model. The caller refuses to boot on
// any problem.
func (v *SchemaValidator) Validate(ctx context.Context) ([]string, error) {
	var problems []string

	// 1. Every embedded migration must be applied. A binary running ahead of its
	//    migrations — a rolling deployment, or an interrupted migration — would
	//    query columns and policies that may not exist yet.
	pending, err := migrate.Pending(ctx, v.pool)
	if err != nil {
		return nil, fmt.Errorf("check pending migrations: %w", err)
	}
	if len(pending) > 0 {
		problems = append(problems, fmt.Sprintf(
			"%d migration(s) this binary requires are not applied (e.g. %s); the binary is ahead of the schema",
			len(pending), pending[0]))
	}

	// 2. Row-level security must be enabled AND forced, with a policy, on every
	//    tenant-owned table. Enabled-without-forced lets the owning role bypass
	//    it; disabled removes isolation entirely; no policy under FORCE denies
	//    all, which is safe but broken. All three are reported.
	for _, table := range TenantOwnedTables {
		var enabled, forced bool
		var policies int
		err := v.pool.QueryRow(ctx, `
			SELECT c.relrowsecurity, c.relforcerowsecurity,
			       (SELECT count(*) FROM pg_policies p
			         WHERE p.schemaname = current_schema() AND p.tablename = c.relname)
			  FROM pg_class c
			  JOIN pg_namespace n ON n.oid = c.relnamespace
			 WHERE n.nspname = current_schema() AND c.relname = $1`,
			table).Scan(&enabled, &forced, &policies)
		if err != nil {
			// A missing table is itself a broken invariant on a schema the
			// binary expects.
			problems = append(problems, fmt.Sprintf("table %s is absent or unreadable: %v", table, err))
			continue
		}
		if !enabled {
			problems = append(problems, fmt.Sprintf("%s does not have row-level security enabled — tenant isolation is off", table))
		}
		if !forced {
			problems = append(problems, fmt.Sprintf("%s does not FORCE row-level security — the owning role bypasses isolation", table))
		}
		if policies == 0 {
			problems = append(problems, fmt.Sprintf("%s has no row-level-security policy", table))
		}

		// 3. tenant_id must exist and be NOT NULL. A nullable ownership column
		//    makes ownership optional; a NULL owner resolves to nothing and would
		//    slip past the referential checks.
		var nullable string
		err = v.pool.QueryRow(ctx, `
			SELECT is_nullable FROM information_schema.columns
			 WHERE table_schema = current_schema() AND table_name = $1 AND column_name = 'tenant_id'`,
			table).Scan(&nullable)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s has no tenant_id column: %v", table, err))
			continue
		}
		if nullable != "NO" {
			problems = append(problems, fmt.Sprintf("%s.tenant_id is nullable — ownership must be mandatory", table))
		}
	}

	// 4. Audit immutability. ADR-TENANCY-003/004 authority rests on audit_event
	//    being append-only: if a row's tenant_id could be rewritten after commit,
	//    the resolver's cross-check against the governed object could be forced
	//    to agree with a forged value. The guarantee is enforced by triggers, and
	//    a trigger is droppable by one operator ALTER — so its presence is a
	//    schema invariant the platform must verify before it trusts the log.
	auditProblems, err := v.validateAuditImmutability(ctx)
	if err != nil {
		return nil, err
	}
	problems = append(problems, auditProblems...)

	return problems, nil
}

// immutableAuditTables are the append-only tables whose guards this validator
// enforces, with the trigger names each one carries. audit_event holds the
// constitutional chain; admin_audit_event holds the administrative chain
// (ADR-AUDIT-007 §6.8). They are separate stores by decision, but there is one
// registry of what "append-only" means, so a table added here is checked at
// boot and by the sentinel without a second validator being written.
var immutableAuditTables = []struct {
	parent      string
	mutateTrg   string
	truncateTrg string
	// alwaysRequired is true when the guards must be ENABLE ALWAYS
	// (pg_trigger.tgenabled = 'A'). Origin-mode guards are suppressed by
	// session_replication_role = 'replica', so for a table that has been
	// hardened, a silent downgrade to 'O' re-opens that bypass and must be
	// reported. audit_event was hardened by 20260809000001 (ADR-AUDIT-008):
	// both its guards, and both on every partition, are ENABLE ALWAYS, so a
	// downgrade to 'O' is now a reported problem rather than the status quo.
	alwaysRequired bool
}{
	{"audit_event", "trg_audit_event_no_row_mutate", "trg_audit_event_no_truncate", true},
	{"admin_audit_event", "trg_admin_audit_event_no_row_mutate", "trg_admin_audit_event_no_truncate", true},
	// asset_change_history (E1.3): tenant-owned CMDB change history, armed
	// ENABLE ALWAYS from its first migration rather than hardened after the
	// fact — see 20260818000001_asset_lifecycle.sql.
	{"asset_change_history", "trg_asset_change_history_no_row_mutate", "trg_asset_change_history_no_truncate", true},
	// incident_event (E5.1): tenant-owned ITSM case timeline, armed ENABLE
	// ALWAYS from its first migration, exactly like asset_change_history —
	// see 20260825000001_incident.sql.
	{"incident_event", "trg_incident_event_no_row_mutate", "trg_incident_event_no_truncate", true},
}

// validateAuditImmutability verifies that every append-only audit table, and
// every one of its partitions, carries both guards: the row-level guard against
// UPDATE/DELETE and the statement-level guard against TRUNCATE. Partitions are
// checked individually because PostgreSQL does not propagate a statement-level
// TRUNCATE trigger from the parent — the same reason migration 20260728000002
// attaches the truncate guard partition by partition.
//
// A trigger is counted only when it is actually armed. pg_trigger.tgenabled is
// 'D' for a disabled trigger, and the catalogue row survives disabling — so a
// check that merely counted rows by name reported a tampered table as healthy.
// Measured against a live database: ALTER TABLE audit_event DISABLE TRIGGER
// trg_audit_event_no_row_mutate left both counts at 1, and both the boot gate
// and the 30-second sentinel saw nothing. Disabled and absent are reported
// separately because they are different operator errors with the same effect.
func (v *SchemaValidator) validateAuditImmutability(ctx context.Context) ([]string, error) {
	var problems []string

	for _, subject := range immutableAuditTables {
		// Three counters per guard, each load-bearing, because pg_trigger.tgenabled
		// has four values and only one of them is a missing trigger:
		//
		//   'O' origin  — fires normally; suppressed by session_replication_role
		//   'A' always  — fires regardless of replication role
		//   'R' replica — fires ONLY in replica mode, i.e. NEVER during application
		//                 traffic. A plain UPDATE succeeds with no session setting.
		//   'D' disabled— never fires
		//
		// Counting merely "not disabled" reported 'R' as healthy, which was
		// measured live: one ALTER TABLE ... ENABLE REPLICA TRIGGER left the
		// counter at 1 while an ordinary UPDATE rewrote audit rows. So "armed"
		// means IN ('O','A'); "alwaysArmed" means 'A'; and "present" ignores mode
		// so a dropped guard can be told apart from a switched-off one.
		rows, err := v.pool.Query(ctx, `
			WITH rels AS (
				SELECT to_regclass(current_schema() || '.' || $1) AS oid
				UNION ALL
				SELECT inhrelid FROM pg_inherits
				 WHERE inhparent = to_regclass(current_schema() || '.' || $1)
			)
			SELECT r.oid::regclass::text,
			       count(*) FILTER (WHERE t.tgname = $2 AND t.tgenabled IN ('O','A')),
			       count(*) FILTER (WHERE t.tgname = $3 AND t.tgenabled IN ('O','A')),
			       count(*) FILTER (WHERE t.tgname = $2 AND t.tgenabled = 'A'),
			       count(*) FILTER (WHERE t.tgname = $3 AND t.tgenabled = 'A'),
			       count(*) FILTER (WHERE t.tgname = $2),
			       count(*) FILTER (WHERE t.tgname = $3)
			  FROM rels r
			  LEFT JOIN pg_trigger t ON t.tgrelid = r.oid AND NOT t.tgisinternal
			 WHERE r.oid IS NOT NULL
			 GROUP BY r.oid`, subject.parent, subject.mutateTrg, subject.truncateTrg)
		if err != nil {
			return nil, fmt.Errorf("audit immutability check (%s): %w", subject.parent, err)
		}

		seen := 0
		for rows.Next() {
			var rel string
			var mutateArmed, truncateArmed int
			var mutateAlways, truncateAlways int
			var mutatePresent, truncatePresent int
			if err := rows.Scan(&rel, &mutateArmed, &truncateArmed,
				&mutateAlways, &truncateAlways, &mutatePresent, &truncatePresent); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan audit trigger row (%s): %w", subject.parent, err)
			}
			seen++
			problems = append(problems, guardProblems(rel, subject.mutateTrg,
				"UPDATE/DELETE", "audit history could be rewritten",
				mutateArmed, mutateAlways, mutatePresent, subject.alwaysRequired)...)
			problems = append(problems, guardProblems(rel, subject.truncateTrg,
				"TRUNCATE", "audit history could be erased",
				truncateArmed, truncateAlways, truncatePresent, subject.alwaysRequired)...)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate audit triggers (%s): %w", subject.parent, err)
		}
		rows.Close()

		if seen == 0 {
			problems = append(problems, fmt.Sprintf(
				"%s is absent — an append-only audit table the platform depends on does not exist", subject.parent))
		}
	}

	return problems, nil
}

// guardProblems classifies one append-only guard on one relation. The four
// outcomes are distinct operator errors with distinct repairs, and conflating
// them sends the operator to the wrong place: absent means a missing migration,
// disabled and replica-mode mean an ALTER TABLE someone ran, and origin-mode on
// a hardened table means the ENABLE ALWAYS was undone.
func guardProblems(rel, trigger, verbs, consequence string, armed, always, present int, alwaysRequired bool) []string {
	switch {
	case present == 0:
		return []string{fmt.Sprintf(
			"%s has no append-only guard against %s — %s", rel, verbs, consequence)}
	case armed == 0:
		// Present but not armed: either 'D' (never fires) or 'R' (fires only
		// under replica mode, so never during application traffic). Both leave
		// the table writable in an ordinary session.
		return []string{fmt.Sprintf(
			"%s has a DISABLED append-only guard against %s (%s) — %s", rel, trigger, verbs, consequence)}
	case alwaysRequired && always == 0:
		return []string{fmt.Sprintf(
			"%s has a DOWNGRADED append-only guard against %s (%s): it is armed but not ENABLE ALWAYS, "+
				"so session_replication_role='replica' suppresses it — %s", rel, trigger, verbs, consequence)}
	}
	return nil
}
