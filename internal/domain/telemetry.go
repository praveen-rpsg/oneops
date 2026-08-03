package domain

import (
	"context"
	"math"
	"strings"
	"time"
)

// MaxTelemetryMetricLength bounds a metric name, the same reason
// MaxAssetTypeLength bounds Asset.Type: it reaches a filter, an index and a
// console label.
const MaxTelemetryMetricLength = 100

// telemetryMetricPattern is the format Sample.Metric must satisfy — the same
// lower-case snake_case discipline assetTypePattern applies to Asset.Type,
// for the same reason (safe to appear in a filter, an index, a route).
var telemetryMetricPattern = assetTypePattern

// MaxTelemetryLabelCount bounds how many labels one sample may carry.
// Unbounded labels would let one malformed producer turn a single sample
// into an unbounded jsonb payload (docs/PLATFORM-BUILD-PLAN.md §3's
// "no unbounded" rule applies to a single write, not only to a scan).
const MaxTelemetryLabelCount = 20

// MaxTelemetryLabelKeyLength and MaxTelemetryLabelValueLength bound each
// label, the same shape MaxAssetNameLength bounds a free-text field.
const (
	MaxTelemetryLabelKeyLength   = 100
	MaxTelemetryLabelValueLength = 200
)

// Sample is one metric observation tied to a Configuration Item (E2.1): the
// first monitoring signal, and the spine E3 alerting will derive from. It is
// deliberately NOT a reified Metric or Alert entity — those are later
// increments (E3) — a Sample is only the fact "this asset reported this
// value for this metric at this time".
type Sample struct {
	TenantID string
	// AssetID names the Configuration Item this observation is about. It is
	// re-verified against the caller's own tenant-scoped connection before
	// being written — see TelemetryRepository.WriteSamples's doc comment —
	// for the same reason AssetRepository.CreateRelationship re-verifies
	// from_asset_id/to_asset_id (ADR-ASSET-001 §6): PostgreSQL's foreign-key
	// checks bypass row-level security on the referenced table, so the
	// foreign key alone cannot be trusted to keep this cross-tenant-safe.
	AssetID   string
	Metric    string
	Value     float64
	Timestamp time.Time
	// Labels carries additional dimensions (e.g. {"host": "db-1", "region":
	// "us-east-1"}) — bounded by MaxTelemetryLabelCount so one sample cannot
	// become an unbounded payload. Nil is stored as an empty object, the same
	// convention Asset.Attributes uses.
	Labels map[string]string
}

// Validate enforces the invariants the database also enforces, so a bad
// sample fails with a field-level reason before it reaches the store.
func (s *Sample) Validate() error {
	if strings.TrimSpace(s.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(s.AssetID) == "" {
		return newValidation("asset_id", "must not be empty")
	}
	metric := strings.TrimSpace(s.Metric)
	if metric == "" {
		return newValidation("metric", "must not be empty")
	}
	if len(metric) > MaxTelemetryMetricLength {
		return newValidation("metric", "must be at most 100 characters")
	}
	if !telemetryMetricPattern.MatchString(metric) {
		return newValidation("metric", "must be lower-case snake_case, e.g. cpu_utilization, disk_free_bytes")
	}
	if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
		return newValidation("value", "must be a finite number")
	}
	if s.Timestamp.IsZero() {
		return newValidation("timestamp", "must not be empty")
	}
	if len(s.Labels) > MaxTelemetryLabelCount {
		return newValidation("labels", "must carry at most 20 labels")
	}
	for k, v := range s.Labels {
		if strings.TrimSpace(k) == "" {
			return newValidation("labels", "a label key must not be blank")
		}
		if len(k) > MaxTelemetryLabelKeyLength {
			return newValidation("labels", "a label key must be at most 100 characters")
		}
		if len(v) > MaxTelemetryLabelValueLength {
			return newValidation("labels", "a label value must be at most 200 characters")
		}
	}
	return nil
}

// NewSample builds a validated Sample. tenantID is always taken from the
// bound connection's context, never from request data — the same rule
// domain.NewAsset follows — so a caller cannot plant a sample inside another
// tenant's boundary.
func NewSample(tenantID, assetID, metric string, value float64, ts time.Time, labels map[string]string) (*Sample, error) {
	if labels == nil {
		labels = map[string]string{}
	}
	s := &Sample{
		TenantID:  strings.TrimSpace(tenantID),
		AssetID:   strings.TrimSpace(assetID),
		Metric:    strings.TrimSpace(metric),
		Value:     value,
		Timestamp: ts,
		Labels:    labels,
	}
	if err := s.Validate(); err != nil {
		return nil, err
	}
	return s, nil
}

// SampleWriteResult is the outcome of writing one Sample from a batch —
// always one per input sample, at that sample's input index, the same shape
// AssetImportResult uses for bulk import (E1.4): one bad sample (a
// validation failure, or an asset_id the tenant cannot see) must not abort
// the samples around it.
type SampleWriteResult struct {
	Accepted bool
	// Reason is empty when Accepted; otherwise the validation or ownership
	// failure that refused this sample.
	Reason string
}

// DefaultTelemetryQueryLimit and MaxTelemetryQueryLimit bound
// TelemetryRepository.QueryRange. Telemetry is the platform's highest-volume
// data path (docs/PLATFORM-BUILD-PLAN.md §3), so the ceiling is higher than
// the CMDB's (maxAssetPageSize = 500), but it is still a ceiling: a caller
// with a wider window than one page fits paginates with `after`, exactly as
// every other keyset-paginated list in this schema does.
const (
	DefaultTelemetryQueryLimit = 500
	MaxTelemetryQueryLimit     = 5000
)

// MaxTelemetryIngestBatch bounds one ingest call, the same "no unbounded
// memory/query" rule maxAssetImportBatch already enforces for bulk asset
// import.
const MaxTelemetryIngestBatch = 1000

// ParseTelemetryTimestamp parses a transport-layer RFC3339 timestamp
// parameter (from/to/after). Used only at the transport boundary;
// TelemetryRepository.QueryRange takes the parsed time.Time, never the raw
// string — the same split ParseAssetStaleAfter draws for the CMDB health
// report.
func ParseTelemetryTimestamp(field, raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, newValidation(field, "must be an RFC3339 timestamp, e.g. 2026-08-01T00:00:00Z")
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, newValidation(field, "must be an RFC3339 timestamp, e.g. 2026-08-01T00:00:00Z")
	}
	return t, nil
}

// TelemetryRepository administers ingestion and range queries over metric
// time-series tied to Configuration Items (E2.1) — the first monitoring
// signal, and the spine E3 alerting derives from.
//
// telemetry_sample is TENANT-OWNED: it is in TenantOwnedTables and carries
// row-level security, so this repository takes no tenant argument anywhere —
// the bound connection already is the boundary (ADR-TENANCY-002). It is
// backed by a TimescaleDB hypertable behind this interface (ADR-TELEMETRY-001
// D1): a hypertable is a regular table under the hood, so row-level security
// applies unchanged, and the interface itself carries nothing
// Timescale-specific, so a dedicated TSDB can replace the implementation
// later without moving this contract.
type TelemetryRepository interface {
	// WriteSamples validates and writes a batch, returning one
	// SampleWriteResult per input sample, in input order — see
	// SampleWriteResult's doc comment. AssetID is re-verified against this
	// store's own tenant-scoped connection before any row naming it is
	// written: an asset_id the caller's tenant cannot see (because it does
	// not exist, or belongs to another tenant) is reported as a rejected
	// sample, not as a row naming a cross-tenant asset (ADR-ASSET-001 §6,
	// extended here). Re-writing an identical (tenant, asset, metric,
	// timestamp) sample is idempotent: it is accepted again and creates no
	// second row. Batch size is bounded by the transport layer
	// (MaxTelemetryIngestBatch); this method itself imposes no additional
	// cap, the same split AssetRepository.Upsert draws with importAssets.
	WriteSamples(ctx context.Context, samples []Sample) ([]SampleWriteResult, error)

	// QueryRange returns a bounded, keyset-paginated page of one asset's one
	// metric, ordered by Timestamp ascending, within [from, to]. after is the
	// Timestamp of the last sample returned by the previous page, exclusive;
	// the zero time.Time means "from the start of the window". limit is
	// clamped to (0, MaxTelemetryQueryLimit] the same way every other bounded
	// list in this schema clamps its page size. A caller with more samples
	// than one page fits re-queries with after set to the last Timestamp
	// received.
	QueryRange(ctx context.Context, assetID, metric string, from, to time.Time, limit int, after time.Time) ([]Sample, error)
}
