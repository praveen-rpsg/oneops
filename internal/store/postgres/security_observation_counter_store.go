package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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
// exactly CountForTenant/RecentForTenant means SecurityObservationStore's
// QueryRange (RLS-scoped by design, unsafe on a privileged connection) and
// WriteObservations (an asset-existence probe) are never reachable from a
// privileged instance, so neither needs a privilegedReadExemptions entry.
//
// RecentForTenant (E8.2b) was added here rather than as a new sibling type:
// it is the IOCMatcher's own privileged read, and both callers (Detector,
// IOCMatcher) are wired over the exact same underlying instance in
// cmd/controlplane/main.go — one privileged type, two narrow ports
// (ObservationCounter and security.ObservationWindowReader), mirroring how
// *postgres.IncidentStore already satisfies two IncidentCorrelator ports.
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

var _ security.ObservationWindowReader = (*SecurityObservationCounterStore)(nil)

// RecentForTenant implements security.ObservationWindowReader (E8.2b, the
// IOCMatcher's own read): a bounded, keyset-paginated page of tenantID's
// security_observation rows across EVERY asset/type in (from, to], ordered by
// the table's own natural key (observed_at, asset_id, observation_type,
// source) — see that interface method's own doc comment for why this natural
// key, compared as a whole via a Postgres ROW value, is the only usable
// cursor once asset_id/observation_type are no longer fixed to one pair
// (contrast QueryRange/QueryRangeForTenant, which fix both and so can key on
// observed_at alone). The lower bound is EXCLUSIVE ("> $2", not ">="),
// unlike CountForTenant's inclusive from: IOCMatcherConfig.Interval's own doc
// comment relies on consecutive tiled windows never double-returning the row
// observed exactly at the shared boundary.
func (s *SecurityObservationCounterStore) RecentForTenant(
	ctx context.Context, tenantID string, from, to time.Time, limit int, after *domain.SecurityObservation,
) ([]domain.SecurityObservation, error) {
	limit = clampSecurityObservationQueryLimit(limit)

	// Two separate statements (rather than one with nullable cursor params):
	// a NULL keyset cursor mixed into a four-column ROW comparison leaves
	// three of its four columns (asset_id/observation_type/source) with no
	// other type context in the statement for Postgres to resolve their
	// parameter type from, which the extended query protocol pgx uses cannot
	// paper over the way a plain-text client could. Branching keeps every
	// parameter's type unambiguous in both statements.
	var (
		rows pgx.Rows
		err  error
	)
	if after == nil {
		rows, err = s.pool.Query(ctx, `
			SELECT tenant_id, asset_id, observation_type, source, severity, observed_at, attributes
			  FROM security_observation
			 WHERE tenant_id = $1
			   AND observed_at > $2 AND observed_at <= $3
			 ORDER BY observed_at, asset_id, observation_type, source
			 LIMIT $4`,
			tenantID, from, to, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT tenant_id, asset_id, observation_type, source, severity, observed_at, attributes
			  FROM security_observation
			 WHERE tenant_id = $1
			   AND observed_at > $2 AND observed_at <= $3
			   AND (observed_at, asset_id, observation_type, source) > ($4, $5, $6, $7)
			 ORDER BY observed_at, asset_id, observation_type, source
			 LIMIT $8`,
			tenantID, from, to, after.ObservedAt, after.AssetID, after.ObservationType, after.Source, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("query recent security observations for tenant: %w", err)
	}
	defer rows.Close()

	out := make([]domain.SecurityObservation, 0, limit)
	for rows.Next() {
		o, err := scanSecurityObservation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan security observation: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent security observations: %w", err)
	}
	return out, nil
}
