package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/security"
)

// IOCMatcherStore is the leader-gated IOCMatcher's own privileged surface
// over ioc (security.IOCLister, E8.2b) — built over the PRIVILEGED pool,
// DELIBERATELY SEPARATE from IOCStore (the admin CRUD API, tenant-scoped)
// rather than one dual-role struct — mirrors SecurityRuleDetectorStore's own
// doc comment (and reason) exactly: keeping this type's method set to
// exactly TenantsWithEnabledIOCs/EnabledIOCsForTenant means IOCStore.Create's
// duplicate-check insert and the rest of the admin CRUD surface are never
// reachable from a privileged instance, so neither needs a
// privilegedReadExemptions entry.
//
// ioc is TENANT-OWNED and carries row-level security; every query here
// carries an EXPLICIT tenant_id predicate or is deliberately tenant-agnostic
// (TenantsWithEnabledIOCs, which discloses only which tenants have work,
// never any tenant's row data) — this connection has row-level security
// switched off (ADR-TENANCY-012).
type IOCMatcherStore struct {
	pool *pgxpool.Pool
}

// NewIOCMatcherStore builds the store over the given (privileged) pool.
func NewIOCMatcherStore(pool *pgxpool.Pool) *IOCMatcherStore {
	return &IOCMatcherStore{pool: pool}
}

var _ security.IOCLister = (*IOCMatcherStore)(nil)

// TenantsWithEnabledIOCs implements security.IOCLister — mirrors
// IncidentGroupingStore.TenantsWithOpenAlertIncidents: tenant-agnostic,
// discloses only which tenants have work.
func (s *IOCMatcherStore) TenantsWithEnabledIOCs(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT tenant_id
		  FROM ioc
		 WHERE enabled = true
		 ORDER BY tenant_id`)
	if err != nil {
		return nil, fmt.Errorf("list tenants with enabled iocs: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, fmt.Errorf("scan tenant_id: %w", err)
		}
		out = append(out, tenantID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenants with enabled iocs: %w", err)
	}
	return out, nil
}

// EnabledIOCsForTenant implements security.IOCLister: up to limit of
// tenantID's enabled iocs, keyset-paginated over ioc_id, with an EXPLICIT
// tenant_id predicate (ADR-TENANCY-012).
func (s *IOCMatcherStore) EnabledIOCsForTenant(ctx context.Context, tenantID string, limit int, after string) ([]*domain.IOC, error) {
	limit = clampIOCPage(limit)
	rows, err := s.pool.Query(ctx, `
		SELECT `+iocCols+`
		  FROM ioc
		 WHERE tenant_id = $1 AND enabled = true AND ($2 = '' OR ioc_id > $2)
		 ORDER BY ioc_id
		 LIMIT $3`, tenantID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("list enabled iocs for tenant: %w", err)
	}
	defer rows.Close()

	out := make([]*domain.IOC, 0, limit)
	for rows.Next() {
		i, err := scanIOC(rows)
		if err != nil {
			return nil, fmt.Errorf("scan ioc: %w", err)
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate enabled iocs for tenant: %w", err)
	}
	return out, nil
}
