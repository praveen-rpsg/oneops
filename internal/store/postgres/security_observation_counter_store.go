package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
	"github.com/rpsg/oneops/internal/security"
)

// SecurityObservationCounterStore is the leader-gated SecurityDetector's own
// privileged read over security_observation (security.ObservationCounter,
// E8.1b-2) — built over the PRIVILEGED pool, DELIBERATELY SEPARATE from
// SecurityObservationStore (the admin ingest/query API, tenant-scoped)
// rather than reusing that type privileged. See SecurityRuleStore's own doc
// comment for the identical reasoning: keeping this type's method set to
// exactly CountForTenant means SecurityObservationStore's QueryRange
// (RLS-scoped by design, unsafe on a privileged connection) and
// WriteObservations (an asset-existence probe) are never reachable from a
// privileged instance, so neither needs a privilegedReadExemptions entry.
type SecurityObservationCounterStore struct {
	pool *pgxpool.Pool
}

// NewSecurityObservationCounterStore builds the store over the given
// (privileged) pool.
func NewSecurityObservationCounterStore(pool *pgxpool.Pool) *SecurityObservationCounterStore {
	return &SecurityObservationCounterStore{pool: pool}
}

var _ security.ObservationCounter = (*SecurityObservationCounterStore)(nil)

// CountForTenant implements security.ObservationCounter — the identical
// bounded COUNT(*), with an explicit tenant_id predicate, that
// SecurityObservationStore.CountForTenant implements for
// domain.SecurityObservationRepository's interface completeness; see that
// method's own doc comment for why the query is duplicated here rather than
// shared through a package-level helper.
func (s *SecurityObservationCounterStore) CountForTenant(
	ctx context.Context, tenantID, assetID, observationType string, minSeverity domain.ObservationSeverity, from, to time.Time,
) (int, error) {
	var count int
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		  FROM security_observation
		 WHERE tenant_id = $1 AND asset_id = $2 AND observation_type = $3
		   AND observed_at >= $4 AND observed_at <= $5
		   AND (CASE severity
		          WHEN 'info' THEN 0 WHEN 'low' THEN 1 WHEN 'medium' THEN 2
		          WHEN 'high' THEN 3 WHEN 'critical' THEN 4 ELSE -1 END) >=
		       (CASE $6::text
		          WHEN 'info' THEN 0 WHEN 'low' THEN 1 WHEN 'medium' THEN 2
		          WHEN 'high' THEN 3 WHEN 'critical' THEN 4 ELSE -1 END)`,
		tenantID, assetID, observationType, from, to, string(minSeverity),
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count security observations for tenant: %w", err)
	}
	return count, nil
}
