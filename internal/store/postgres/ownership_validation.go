package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// OwnershipValidator runs the startup checks that turn recovery into a
// verification boundary. A restored database may be internally inconsistent —
// partial snapshots, tables restored with foreign-key triggers disabled, manual
// SQL imports — and the platform must prove ownership can be established
// unambiguously before it accepts traffic, rather than start and fail in the
// dark (ADR-TENANCY-004, ADR-TENANCY-006).
type OwnershipValidator struct {
	pool *pgxpool.Pool
}

// NewOwnershipValidator builds a validator over the privileged pool.
func NewOwnershipValidator(pool *pgxpool.Pool) *OwnershipValidator {
	return &OwnershipValidator{pool: pool}
}

// danglingCheck is one referential-integrity probe: rows in a tenant-owned table
// whose tenant_id has no matching row in the tenant registry.
type danglingCheck struct {
	table string
	// key is the column reported as an example, so an operator can locate the
	// offending rows.
	key string
}

// referentialChecks enumerates every tenant-owned table whose ownership must
// reference a live tenant. Foreign keys enforce this during normal operation,
// but a restore with triggers disabled bypasses them, and audit_event carries no
// foreign key at all because it is partitioned — so startup re-verifies what the
// constraints normally guarantee.
var referentialChecks = []danglingCheck{
	{"configuration_object", "cfg_id"},
	{"audit_event", "chain_id"},
	{"webhook", "id"},
	{"webhook_delivery", "id"},
	{"policy", "id"},
	{"policy_execution", "id"},
}

// Validate returns one problem string per category that has dangling ownership,
// plus the divergence check from ADR-TENANCY-004. An empty slice means the
// ownership graph is sound. The caller refuses to boot on any problem.
func (v *OwnershipValidator) Validate(ctx context.Context) ([]string, error) {
	var problems []string

	// Divergence: the audit log disagrees with the governed object about who
	// owns a chain (ADR-TENANCY-004).
	var divergent int
	var divExample string
	if err := v.pool.QueryRow(ctx, `
		SELECT count(*), COALESCE(min(ae.chain_id), '')
		  FROM audit_event ae
		  JOIN configuration_object co ON co.cfg_id = ae.chain_id
		 WHERE ae.tenant_id <> co.tenant_id`).Scan(&divergent, &divExample); err != nil {
		return nil, fmt.Errorf("divergence check: %w", err)
	}
	if divergent > 0 {
		problems = append(problems, fmt.Sprintf(
			"audit ownership diverges from the governed object on %d event(s) (e.g. chain %s)",
			divergent, divExample))
	}

	// Dangling ownership: data owned by a tenant that is not in the registry.
	for _, c := range referentialChecks {
		var n int
		var example string
		q := fmt.Sprintf(`
			SELECT count(*), COALESCE(min(%s), '')
			  FROM %s x
			 WHERE NOT EXISTS (SELECT 1 FROM tenant t WHERE t.tenant_id = x.tenant_id)`,
			c.key, c.table)
		if err := v.pool.QueryRow(ctx, q).Scan(&n, &example); err != nil {
			return nil, fmt.Errorf("dangling check on %s: %w", c.table, err)
		}
		if n > 0 {
			problems = append(problems, fmt.Sprintf(
				"%s has %d row(s) owned by a tenant not in the registry (e.g. %s=%s)",
				c.table, n, c.key, example))
		}
	}

	return problems, nil
}
