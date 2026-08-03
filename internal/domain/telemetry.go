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
	// Min, Max and Count are set only when this Sample was returned at
	// ResolutionRollup5m (E2.1b): Value then holds the bucket's average, Min
	// and Max its extremes, and Count how many raw samples it summarises.
	// nil/nil/nil is how a caller tells a raw observation from a rollup
	// bucket apart — a raw Sample never sets these, and a rollup bucket
	// always sets all three together.
	Min   *float64
	Max   *float64
	Count *int64
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

// TelemetryResolution selects which granularity TelemetryRepository.QueryRange
// reads from (E2.1b).
type TelemetryResolution string

const (
	// ResolutionAuto ("") lets the store pick: raw for a window no wider than
	// AutoResolutionRawWindow, the rollup otherwise. This is the default a
	// caller gets by sending no resolution parameter at all.
	ResolutionAuto TelemetryResolution = ""
	// ResolutionRaw requests individual samples. Rejected for a window wider
	// than MaxRawQueryWindow — the bounded-queries rule
	// (docs/PLATFORM-BUILD-PLAN.md §3) applies to resolution choice, not only
	// to page size: a caller may not force a full year of per-sample rows
	// through one keyset-paginated query path.
	ResolutionRaw TelemetryResolution = "raw"
	// ResolutionRollup5m requests the 5-minute avg/min/max/count rollup
	// (telemetry_rollup_5m) instead of raw samples.
	ResolutionRollup5m TelemetryResolution = "rollup_5m"
)

// TelemetryRollupBucket is the rollup's bucket width. It is a domain constant,
// not merely a store detail, because AutoResolutionRawWindow and a caller's
// expectations about rollup timestamp alignment both depend on it.
const TelemetryRollupBucket = 5 * time.Minute

// AutoResolutionRawWindow is the window width at or below which
// ResolutionAuto reads raw samples; above it, ResolutionAuto reads the
// rollup. Six hours is short enough that per-sample detail is still the
// useful answer (a dashboard's "last few hours" view), and long enough that a
// single business day's incident review does not silently fall back to
// coarse buckets.
const AutoResolutionRawWindow = 6 * time.Hour

// MaxRawQueryWindow bounds an EXPLICIT ResolutionRaw request. A caller asking
// for raw samples over, say, a year would force a scan and a page-by-page
// walk across a dataset the rollup exists precisely to summarise instead;
// MaxTelemetryQueryLimit bounds one page's row count, not how many chunks a
// wide raw query has to touch to fill even one page.
const MaxRawQueryWindow = 7 * 24 * time.Hour

// ParseTelemetryResolution validates a transport-layer `resolution` query
// parameter. Used only at the transport boundary, the same split
// ParseTelemetryTimestamp draws for from/to/after.
func ParseTelemetryResolution(raw string) (TelemetryResolution, error) {
	switch TelemetryResolution(strings.TrimSpace(raw)) {
	case ResolutionAuto, ResolutionRaw, ResolutionRollup5m:
		return TelemetryResolution(strings.TrimSpace(raw)), nil
	default:
		return "", newValidation("resolution", `must be "raw", "rollup_5m", or omitted for automatic selection`)
	}
}

// DefaultTelemetryRawRetentionDays and DefaultTelemetryRollupRetentionDays are
// TelemetryRawRetentionDaysKey's registry default and the rollup's fixed
// retention (E2.1b, ADR-TELEMETRY-002). Only the raw horizon is an operator
// tunable — see TelemetryRawRetentionDaysKey's doc comment for why the rollup
// horizon is not.
const (
	DefaultTelemetryRawRetentionDays    = 90
	DefaultTelemetryRollupRetentionDays = 400
)

// TelemetryRawRetentionDaysKey names the SettingDefinitions entry that bounds
// how long a raw telemetry_sample row is kept before the retention worker
// drops its chunk (E2.1b, ADR-TELEMETRY-002).
//
// This setting is PLATFORM-scoped, not per-tenant, even though it is stored
// in the same tenant-owned `setting` table every other setting uses: the
// retention worker reads it ONLY from SystemTenantID's override (falling back
// to DefaultTelemetryRawRetentionDays when unset) and applies the result to
// the whole telemetry_sample hypertable in one pass. A hypertable's chunks
// span every tenant's rows together — dropping a chunk older than the cutoff
// drops it for all of them at once — so there is no single retention
// operation that could honour ten different tenants' ten different horizons
// without either a hypertable per tenant or a filtered per-tenant DELETE
// worker, neither of which E2.1b builds. A value stored under any tenant
// other than "system" is accepted (Setting.Validate does not know which
// tenant is calling) but has no effect on retention; ADR-TELEMETRY-002
// records true per-tenant retention as a deferred follow-up rather than
// pretending this key delivers it today.
const TelemetryRawRetentionDaysKey = "telemetry_raw_retention_days"

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
	// second row — but idempotent is not the same as "merged". The insert is
	// ON CONFLICT (tenant_id, asset_id, metric, ts) DO NOTHING, so a SECOND
	// sample naming the same key with a DIFFERENT value is silently dropped:
	// the FIRST write for that key wins, permanently, and the caller is told
	// Accepted with no way to distinguish "this exact value was written" from
	// "a different value already occupies this timestamp and yours was
	// discarded". A producer relying on a later correction overwriting an
	// earlier bad reading at the same timestamp will not observe one; it must
	// pick a different timestamp instead. Batch size is bounded by the
	// transport layer (MaxTelemetryIngestBatch); this method itself imposes
	// no additional cap, the same split AssetRepository.Upsert draws with
	// importAssets.
	WriteSamples(ctx context.Context, samples []Sample) ([]SampleWriteResult, error)

	// QueryRange returns a bounded, keyset-paginated page of one asset's one
	// metric, ordered by Timestamp ascending, within [from, to]. after is the
	// Timestamp of the last sample returned by the previous page, exclusive;
	// the zero time.Time means "from the start of the window". limit is
	// clamped to (0, MaxTelemetryQueryLimit] the same way every other bounded
	// list in this schema clamps its page size. A caller with more samples
	// than one page fits re-queries with after set to the last Timestamp
	// received.
	//
	// after's ts-only keyset works ONLY because (tenant_id, asset_id, metric,
	// ts) is the table's actual uniqueness constraint (uq_telemetry_sample) —
	// asset_id and metric are already fixed by this call's own arguments, so
	// ts alone is unique within the page being walked. If a future change
	// widened that key (e.g. a second producer allowed to report the same
	// asset/metric/timestamp pair as a distinct row) this cursor would start
	// silently skipping or repeating rows at a page boundary; anyone changing
	// the natural key must change this pagination scheme in the same commit.
	//
	// resolution selects raw samples or the 5-minute rollup (E2.1b);
	// ResolutionAuto picks based on the [from, to] window's width — see
	// AutoResolutionRawWindow. A rollup row is a Sample with Value set to the
	// bucket's average and Min/Max/Count also populated (see Sample's doc
	// comment); ts is then the bucket's start, and after/pagination work
	// identically because telemetry_rollup_5m carries the same
	// (tenant_id, asset_id, metric, bucket) uniqueness telemetry_sample does.
	QueryRange(ctx context.Context, assetID, metric string, from, to time.Time, limit int, after time.Time, resolution TelemetryResolution) ([]Sample, error)

	// QueryRangeForTenant is QueryRange's privileged-pool counterpart (E3.1).
	// QueryRange's own doc comment states its isolation is "enforced by
	// row-level security on this store's tenant-scoped connection" — it has
	// no tenant predicate of its own because it has never needed one. The
	// alert-rule evaluator is the first caller that reads telemetry from a
	// privileged, cross-tenant connection (the same category of background
	// worker TelemetryRollupWorker already is), where row-level security does
	// not apply at all: calling QueryRange there would filter on nothing but
	// asset_id/metric and trust that no two tenants' rows can ever match
	// them. tenantID is therefore a required, explicit SQL predicate rather
	// than an ambient assumption — the same posture ADR-TENANCY-009 requires
	// of a privileged mutation, applied here to a privileged read. Returns
	// raw samples only, ordered by Timestamp ascending, bounded by
	// MaxTelemetryQueryLimit; there is no resolution/rollup selection because
	// ForDuration (MaxAlertRuleForDurationSeconds = 24h) never approaches
	// AutoResolutionRawWindow's territory.
	QueryRangeForTenant(ctx context.Context, tenantID, assetID, metric string, from, to time.Time) ([]Sample, error)
}
