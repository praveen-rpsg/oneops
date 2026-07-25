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

// validateAuditImmutability verifies that audit_event and every one of its
// partitions carries both append-only guards: the row-level guard against
// UPDATE/DELETE and the statement-level guard against TRUNCATE. Partitions are
// checked individually because PostgreSQL does not propagate a statement-level
// TRUNCATE trigger from the parent — the same reason migration 20260728000002
// attaches the truncate guard partition by partition.
func (v *SchemaValidator) validateAuditImmutability(ctx context.Context) ([]string, error) {
	rows, err := v.pool.Query(ctx, `
		WITH rels AS (
			SELECT to_regclass(current_schema() || '.audit_event') AS oid
			UNION ALL
			SELECT inhrelid FROM pg_inherits
			 WHERE inhparent = to_regclass(current_schema() || '.audit_event')
		)
		SELECT r.oid::regclass::text,
		       count(*) FILTER (WHERE t.tgname = 'trg_audit_event_no_row_mutate'),
		       count(*) FILTER (WHERE t.tgname = 'trg_audit_event_no_truncate')
		  FROM rels r
		  LEFT JOIN pg_trigger t ON t.tgrelid = r.oid AND NOT t.tgisinternal
		 WHERE r.oid IS NOT NULL
		 GROUP BY r.oid`)
	if err != nil {
		return nil, fmt.Errorf("audit immutability check: %w", err)
	}
	defer rows.Close()

	var problems []string
	seen := 0
	for rows.Next() {
		var rel string
		var mutate, truncate int
		if err := rows.Scan(&rel, &mutate, &truncate); err != nil {
			return nil, fmt.Errorf("scan audit trigger row: %w", err)
		}
		seen++
		if mutate == 0 {
			problems = append(problems, fmt.Sprintf(
				"%s has no append-only guard against UPDATE/DELETE — audit ownership could be rewritten", rel))
		}
		if truncate == 0 {
			problems = append(problems, fmt.Sprintf(
				"%s has no append-only guard against TRUNCATE — audit history could be erased", rel))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit triggers: %w", err)
	}
	if seen == 0 {
		problems = append(problems, "audit_event is absent — the audit log the ownership model depends on does not exist")
	}
	return problems, nil
}
