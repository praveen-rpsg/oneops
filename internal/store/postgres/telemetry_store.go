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

// TelemetryStore administers metric time-series tied to Configuration Items
// (E2.1), backed by the telemetry_sample TimescaleDB hypertable
// (ADR-TELEMETRY-001).
//
// telemetry_sample is TENANT-OWNED — it is in TenantOwnedTables and carries
// row-level security — so this store is built from the tenant-scoped pool
// and takes no tenant argument anywhere: the bound connection is the
// boundary, exactly like AssetStore (ADR-TENANCY-002). A hypertable is a
// regular Postgres table to everything below this line; nothing here is
// Timescale-specific beyond the migration that created it.
type TelemetryStore struct {
	pool *pgxpool.Pool
}

// NewTelemetryStore builds the telemetry repository over the given pool.
func NewTelemetryStore(pool *pgxpool.Pool) *TelemetryStore {
	return &TelemetryStore{pool: pool}
}

var _ domain.TelemetryRepository = (*TelemetryStore)(nil)

func clampTelemetryQueryLimit(limit int) int {
	if limit <= 0 {
		return domain.DefaultTelemetryQueryLimit
	}
	if limit > domain.MaxTelemetryQueryLimit {
		return domain.MaxTelemetryQueryLimit
	}
	return limit
}

// WriteSamples validates every sample, re-verifies every referenced asset_id
// against this store's own tenant-scoped connection in one query, and writes
// the survivors in one round trip — see domain.TelemetryRepository's doc
// comment for the full contract.
//
// Two passes over samples, neither touching the database more than once:
// pass one runs Sample.Validate and collects the distinct asset ids it needs
// to check; pass two, after that single existence query, either rejects a
// sample whose asset_id the tenant cannot see or queues it for the batched
// insert. This is the same shape AssetStore.CreateRelationship's existence
// check takes, applied to a whole batch instead of one relationship's two
// endpoints (ADR-ASSET-001 §6, ADR-TELEMETRY-001).
func (s *TelemetryStore) WriteSamples(ctx context.Context, samples []domain.Sample) ([]domain.SampleWriteResult, error) {
	results := make([]domain.SampleWriteResult, len(samples))
	if len(samples) == 0 {
		return results, nil
	}

	assetIDSet := map[string]bool{}
	for i := range samples {
		if err := samples[i].Validate(); err != nil {
			results[i] = domain.SampleWriteResult{Reason: err.Error()}
			continue
		}
		assetIDSet[samples[i].AssetID] = true
	}
	if len(assetIDSet) == 0 {
		// Every sample failed validation; there is nothing left to check or
		// insert.
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
		return nil, fmt.Errorf("check sample asset ids: %w", err)
	}
	visible := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan sample asset id: %w", err)
		}
		visible[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate sample asset ids: %w", err)
	}
	rows.Close()

	batch := &pgx.Batch{}
	var queuedAt []int
	for i := range samples {
		if results[i].Reason != "" {
			continue // already rejected by validation, above
		}
		if !visible[samples[i].AssetID] {
			results[i] = domain.SampleWriteResult{Reason: "asset_id does not name an asset visible to this tenant"}
			continue
		}
		labels, err := json.Marshal(samples[i].Labels)
		if err != nil {
			results[i] = domain.SampleWriteResult{Reason: fmt.Sprintf("marshal labels: %v", err)}
			continue
		}
		// ON CONFLICT DO NOTHING makes a re-sent sample idempotent: retrying a
		// batch after a partial network failure writes no second row and is
		// still reported as accepted (docs/PLATFORM-BUILD-PLAN.md §3's
		// idempotency rule).
		batch.Queue(`
			INSERT INTO telemetry_sample (tenant_id, asset_id, metric, ts, value, labels)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (tenant_id, asset_id, metric, ts) DO NOTHING`,
			samples[i].TenantID, samples[i].AssetID, samples[i].Metric,
			samples[i].Timestamp, samples[i].Value, labels)
		queuedAt = append(queuedAt, i)
	}
	if len(queuedAt) == 0 {
		return results, nil
	}

	br := s.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for _, i := range queuedAt {
		if _, err := br.Exec(); err != nil {
			results[i] = domain.SampleWriteResult{Reason: fmt.Sprintf("write sample: %v", err)}
			continue
		}
		results[i] = domain.SampleWriteResult{Accepted: true}
	}
	return results, nil
}

// QueryRange returns a bounded, keyset-paginated page of one asset's one
// metric — see domain.TelemetryRepository's doc comment for the exact
// pagination and resolution contract. Isolation is enforced by row-level
// security on this store's tenant-scoped connection, exactly like every
// other read in this schema: a caller naming another tenant's asset_id gets
// an empty page, not an error, the same as AssetStore.Get would if asset_id
// resolved to nothing at all. This holds identically for both tables
// resolution can select — telemetry_rollup_5m carries the same
// ENABLE/FORCE ROW LEVEL SECURITY + tenant_isolation policy telemetry_sample
// does (ADR-TELEMETRY-002), not a defensive filter layered on top of it.
func (s *TelemetryStore) QueryRange(
	ctx context.Context, assetID, metric string, from, to time.Time, limit int, after time.Time,
	resolution domain.TelemetryResolution,
) ([]domain.Sample, error) {
	limit = clampTelemetryQueryLimit(limit)

	resolved, err := resolveTelemetryResolution(resolution, from, to)
	if err != nil {
		return nil, err
	}
	if resolved == domain.ResolutionRollup5m {
		return s.queryRollupRange(ctx, assetID, metric, from, to, limit, after)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, asset_id, metric, ts, value, labels
		  FROM telemetry_sample
		 WHERE asset_id = $1 AND metric = $2
		   AND ts >= $3 AND ts <= $4
		   AND ($5::timestamptz IS NULL OR ts > $5)
		 ORDER BY ts
		 LIMIT $6`,
		assetID, metric, from, to, nullTime(after), limit)
	if err != nil {
		return nil, fmt.Errorf("query telemetry range: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Sample, 0, limit)
	for rows.Next() {
		sm, err := scanSample(rows)
		if err != nil {
			return nil, fmt.Errorf("scan telemetry sample: %w", err)
		}
		out = append(out, sm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate telemetry samples: %w", err)
	}
	return out, nil
}

// resolveTelemetryResolution turns the caller's requested resolution into the
// concrete one this query will actually run, and is where
// domain.MaxRawQueryWindow is enforced (E2.1b).
//
// ResolutionAuto is not a synonym for ResolutionRaw with a silent fallback:
// it is a real decision, made once, here, so a caller who never thinks about
// resolution at all still cannot force a wide raw scan by omission — the
// exact failure mode "don't return raw for a 1-year range" describes.
func resolveTelemetryResolution(resolution domain.TelemetryResolution, from, to time.Time) (domain.TelemetryResolution, error) {
	window := to.Sub(from)
	switch resolution {
	case domain.ResolutionRollup5m:
		return domain.ResolutionRollup5m, nil
	case domain.ResolutionRaw:
		if window > domain.MaxRawQueryWindow {
			return "", domain.NewValidationError("resolution",
				fmt.Sprintf("raw resolution is not available for a window wider than %s; "+
					"request rollup_5m, or narrow [from, to]", domain.MaxRawQueryWindow))
		}
		return domain.ResolutionRaw, nil
	case domain.ResolutionAuto:
		if window > domain.AutoResolutionRawWindow {
			return domain.ResolutionRollup5m, nil
		}
		return domain.ResolutionRaw, nil
	default:
		return "", domain.NewValidationError("resolution",
			`must be "raw", "rollup_5m", or omitted for automatic selection`)
	}
}

// queryRollupRange is QueryRange's ResolutionRollup5m path, reading
// telemetry_rollup_5m instead of telemetry_sample. Shape mirrors the raw
// query exactly — same predicate, same ts(bucket)-only keyset cursor — for
// the reason domain.TelemetryRepository's doc comment gives: both tables
// share the same (tenant_id, asset_id, metric, ts) uniqueness.
func (s *TelemetryStore) queryRollupRange(
	ctx context.Context, assetID, metric string, from, to time.Time, limit int, after time.Time,
) ([]domain.Sample, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tenant_id, asset_id, metric, bucket, avg_value, min_value, max_value, sample_count
		  FROM telemetry_rollup_5m
		 WHERE asset_id = $1 AND metric = $2
		   AND bucket >= $3 AND bucket <= $4
		   AND ($5::timestamptz IS NULL OR bucket > $5)
		 ORDER BY bucket
		 LIMIT $6`,
		assetID, metric, from, to, nullTime(after), limit)
	if err != nil {
		return nil, fmt.Errorf("query telemetry rollup range: %w", err)
	}
	defer rows.Close()

	out := make([]domain.Sample, 0, limit)
	for rows.Next() {
		sm, err := scanRollupSample(rows)
		if err != nil {
			return nil, fmt.Errorf("scan telemetry rollup sample: %w", err)
		}
		out = append(out, sm)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate telemetry rollup samples: %w", err)
	}
	return out, nil
}

// QueryRange's optional `after` bound uses the existing nullTime helper
// (webhook_consume_store.go) to bind SQL NULL for the zero time.Time ("no
// cursor set") rather than sending the year-1 sentinel value.

func scanSample(s scanner) (domain.Sample, error) {
	var (
		sm         domain.Sample
		labelBytes []byte
	)
	if err := s.Scan(&sm.TenantID, &sm.AssetID, &sm.Metric, &sm.Timestamp, &sm.Value, &labelBytes); err != nil {
		return domain.Sample{}, err
	}
	sm.Labels = map[string]string{}
	if len(labelBytes) > 0 {
		if err := json.Unmarshal(labelBytes, &sm.Labels); err != nil {
			return domain.Sample{}, fmt.Errorf("unmarshal sample labels: %w", err)
		}
	}
	return sm, nil
}

// scanRollupSample scans one telemetry_rollup_5m row into a Sample with
// Value set to the bucket's average and Min/Max/Count populated — see
// Sample's doc comment for how a caller tells this apart from a raw
// observation.
func scanRollupSample(s scanner) (domain.Sample, error) {
	var (
		sm       domain.Sample
		minValue float64
		maxValue float64
		count    int64
	)
	if err := s.Scan(&sm.TenantID, &sm.AssetID, &sm.Metric, &sm.Timestamp, &sm.Value, &minValue, &maxValue, &count); err != nil {
		return domain.Sample{}, err
	}
	sm.Labels = map[string]string{}
	sm.Min, sm.Max, sm.Count = &minValue, &maxValue, &count
	return sm, nil
}
