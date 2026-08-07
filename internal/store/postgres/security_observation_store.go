package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rpsg/oneops/internal/domain"
)

// SecurityObservationStore administers security_observation: an append-only
// fact table for security-relevant observations tied to Configuration Items
// (E8.1a), backed by a TimescaleDB hypertable that is a SIBLING of
// telemetry_sample, not a reuse of it (ADR-TELEMETRY-001, ADR-SOC-001).
//
// security_observation is TENANT-OWNED — it is in TenantOwnedTables and
// carries row-level security — so this store is built from the tenant-scoped
// pool and takes no tenant argument anywhere: the bound connection is the
// boundary, exactly like TelemetryStore (ADR-TENANCY-002).
type SecurityObservationStore struct {
	pool *pgxpool.Pool
}

// NewSecurityObservationStore builds the store over the given pool.
func NewSecurityObservationStore(pool *pgxpool.Pool) *SecurityObservationStore {
	return &SecurityObservationStore{pool: pool}
}

var _ domain.SecurityObservationRepository = (*SecurityObservationStore)(nil)

func clampSecurityObservationQueryLimit(limit int) int {
	if limit <= 0 {
		return domain.DefaultSecurityObservationQueryLimit
	}
	if limit > domain.MaxSecurityObservationQueryLimit {
		return domain.MaxSecurityObservationQueryLimit
	}
	return limit
}

// WriteObservations validates every observation, re-verifies every
// referenced asset_id against this store's own tenant-scoped connection in
// one query, and writes the survivors in one round trip — see
// domain.SecurityObservationRepository's doc comment for the full contract.
// Mirrors TelemetryStore.WriteSamples exactly: two passes over the batch,
// neither touching the database more than once (ADR-ASSET-001 §6,
// ADR-TELEMETRY-001).
func (s *SecurityObservationStore) WriteObservations(ctx context.Context, observations []domain.SecurityObservation) ([]domain.SecurityObservationWriteResult, error) {
	results := make([]domain.SecurityObservationWriteResult, len(observations))
	if len(observations) == 0 {
		return results, nil
	}

	assetIDSet := map[string]bool{}
	for i := range observations {
		if err := observations[i].Validate(); err != nil {
			results[i] = domain.SecurityObservationWriteResult{Reason: err.Error()}
			continue
		}
		assetIDSet[observations[i].AssetID] = true
	}
	if len(assetIDSet) == 0 {
		// Every observation failed validation; there is nothing left to check
		// or insert.
		return results, nil
	}
	ids := make([]string, 0, len(assetIDSet))
	for id := range assetIDSet {
		ids = append(ids, id)
	}

	// RLS on asset filters this to the caller's own tenant already: an id
	// belonging to another tenant simply does not come back, indistinguishable
	// from one that does not exist at all — the correct answer either way.
	rows, err := s.pool.Query(ctx, `SELECT asset_id FROM asset WHERE asset_id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("check observation asset ids: %w", err)
	}
	visible := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan observation asset id: %w", err)
		}
		visible[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate observation asset ids: %w", err)
	}
	rows.Close()

	batch := &pgx.Batch{}
	var queuedAt []int
	for i := range observations {
		if results[i].Reason != "" {
			continue // already rejected by validation, above
		}
		if !visible[observations[i].AssetID] {
			results[i] = domain.SecurityObservationWriteResult{Reason: "asset_id does not name an asset visible to this tenant"}
			continue
		}
		attrs, err := json.Marshal(observations[i].Attributes)
		if err != nil {
			results[i] = domain.SecurityObservationWriteResult{Reason: fmt.Sprintf("marshal attributes: %v", err)}
			continue
		}
		// ON CONFLICT DO NOTHING makes a re-sent observation idempotent: retrying
		// a batch after a partial network failure writes no second row and is
		// still reported as accepted — the same contract WriteSamples documents.
		batch.Queue(`
			INSERT INTO security_observation (tenant_id, asset_id, observation_type, source, severity, observed_at, attributes)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (tenant_id, asset_id, observation_type, source, observed_at) DO NOTHING`,
			observations[i].TenantID, observations[i].AssetID, observations[i].ObservationType,
			observations[i].Source, string(observations[i].Severity), observations[i].ObservedAt, attrs)
		queuedAt = append(queuedAt, i)
	}
	if len(queuedAt) == 0 {
		return results, nil
	}

	br := s.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for _, i := range queuedAt {
		if _, err := br.Exec(); err != nil {
			results[i] = domain.SecurityObservationWriteResult{Reason: fmt.Sprintf("write observation: %v", err)}
			continue
		}
		results[i] = domain.SecurityObservationWriteResult{Accepted: true}
	}
	return results, nil
}

// QueryRange returns a bounded, keyset-paginated page of one asset's one
// observation type — see domain.SecurityObservationRepository's doc comment
// for the exact pagination contract. Isolation is enforced by row-level
// security on this store's tenant-scoped connection, exactly like
// TelemetryStore.QueryRange: a caller naming another tenant's asset_id gets
// an empty page, not an error.
func (s *SecurityObservationStore) QueryRange(
	ctx context.Context, assetID, observationType string, from, to time.Time, limit int, after time.Time,
) ([]domain.SecurityObservation, error) {
	limit = clampSecurityObservationQueryLimit(limit)

	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, asset_id, observation_type, source, severity, observed_at, attributes
		  FROM security_observation
		 WHERE asset_id = $1 AND observation_type = $2
		   AND observed_at >= $3 AND observed_at <= $4
		   AND ($5::timestamptz IS NULL OR observed_at > $5)
		 ORDER BY observed_at
		 LIMIT $6`,
		assetID, observationType, from, to, nullTime(after), limit)
	if err != nil {
		return nil, fmt.Errorf("query security observation range: %w", err)
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
		return nil, fmt.Errorf("iterate security observations: %w", err)
	}
	return out, nil
}

// QueryRangeForTenant is QueryRange's privileged-pool counterpart — see
// domain.SecurityObservationRepository's doc comment for why an explicit
// tenant_id predicate is required here rather than relied on as an ambient
// connection property. Bounded by MaxSecurityObservationQueryLimit, ordered
// by observed_at ascending — mirrors TelemetryStore.QueryRangeForTenant's
// query shape exactly.
func (s *SecurityObservationStore) QueryRangeForTenant(
	ctx context.Context, tenantID, assetID, observationType string, from, to time.Time,
) ([]domain.SecurityObservation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, asset_id, observation_type, source, severity, observed_at, attributes
		  FROM security_observation
		 WHERE tenant_id = $1 AND asset_id = $2 AND observation_type = $3
		   AND observed_at >= $4 AND observed_at <= $5
		 ORDER BY observed_at
		 LIMIT $6`,
		tenantID, assetID, observationType, from, to, domain.MaxSecurityObservationQueryLimit)
	if err != nil {
		return nil, fmt.Errorf("query security observation range for tenant: %w", err)
	}
	defer rows.Close()

	out := make([]domain.SecurityObservation, 0, 16)
	for rows.Next() {
		o, err := scanSecurityObservation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan security observation: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate security observations: %w", err)
	}
	return out, nil
}

// CountForTenant implements domain.SecurityObservationRepository's detector
// read (E8.1b-2): a single bounded COUNT(*), never a List — see that
// interface method's own doc comment. Present on this type for interface
// completeness, the same reason TelemetryRepository.QueryRangeForTenant sits
// on TelemetryStore even though nothing calls it through this tenant-scoped
// instance in production; the detector's own privileged caller uses
// SecurityObservationCounterStore instead
// (security_observation_counter_store.go) — a SEPARATE, minimal type kept
// off this store's admin-CRUD method set for the identical reason
// SecurityRuleStore's own doc comment explains for security_rule. The query
// itself (including the severity-rank CASE expressions) is duplicated there
// rather than shared through a package-level helper, so
// TestPrivilegedReads_AreScopedToATenant can see the tenant_id predicate
// directly in EACH method's own source text — a helper call site would hide
// the SQL literal from that guard's per-method sweep.
func (s *SecurityObservationStore) CountForTenant(
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

func scanSecurityObservation(s scanner) (domain.SecurityObservation, error) {
	var (
		o         domain.SecurityObservation
		severity  string
		attrBytes []byte
	)
	if err := s.Scan(&o.TenantID, &o.AssetID, &o.ObservationType, &o.Source, &severity, &o.ObservedAt, &attrBytes); err != nil {
		return domain.SecurityObservation{}, err
	}
	o.Severity = domain.ObservationSeverity(severity)
	o.Attributes = map[string]string{}
	if len(attrBytes) > 0 {
		if err := json.Unmarshal(attrBytes, &o.Attributes); err != nil {
			return domain.SecurityObservation{}, fmt.Errorf("unmarshal observation attributes: %w", err)
		}
	}
	return o, nil
}
