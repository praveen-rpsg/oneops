package domain

import (
	"context"
	"strings"
	"time"
)

// MaxObservationTypeLength bounds ObservationType, mirroring
// MaxTelemetryMetricLength for the same reason: it reaches a filter, an
// index and a console label.
const MaxObservationTypeLength = 100

// observationTypePattern reuses assetTypePattern/telemetryMetricPattern: the
// same lower-case snake_case discipline, for the same reason (safe to appear
// in a filter, an index, a route).
var observationTypePattern = assetTypePattern

// MaxObservationSourceLength bounds Source. Unlike ObservationType, Source
// carries no format constraint at the domain level — it names the producer
// (e.g. "wazuh", "cloudtrail", "ids-1"), an open set with no shared grammar —
// so only a generous length bound applies, the same split
// MaxCollectorCheckTargetLength draws for CollectorCheck.Target.
const MaxObservationSourceLength = 200

// MaxObservationAttributeCount, MaxObservationAttributeKeyLength and
// MaxObservationAttributeValueLength bound Attributes, mirroring
// MaxTelemetryLabelCount/MaxTelemetryLabelKeyLength/MaxTelemetryLabelValueLength
// for the identical reason: unbounded attributes would let one malformed
// producer turn a single observation into an unbounded jsonb payload
// (docs/PLATFORM-BUILD-PLAN.md §3's "no unbounded" rule).
const (
	MaxObservationAttributeCount       = 20
	MaxObservationAttributeKeyLength   = 100
	MaxObservationAttributeValueLength = 200
)

// ObservationSeverity classifies how serious a SecurityObservation is. It is
// deliberately a closed, ordered set — unlike ObservationType and Source,
// which are open — because severity is meant to be compared and filtered on
// by later consumers (E8.1b), the same reason AlertSeverity is closed.
// AlertSeverity itself does not fit here: its three-value vocabulary
// (critical/warning/info) is a label for a threshold breach, not the
// five-level scale (info/low/medium/high/critical) security findings are
// conventionally reported at.
type ObservationSeverity string

const (
	ObservationSeverityInfo     ObservationSeverity = "info"
	ObservationSeverityLow      ObservationSeverity = "low"
	ObservationSeverityMedium   ObservationSeverity = "medium"
	ObservationSeverityHigh     ObservationSeverity = "high"
	ObservationSeverityCritical ObservationSeverity = "critical"
)

// Valid reports whether s is a defined severity.
func (s ObservationSeverity) Valid() bool {
	switch s {
	case ObservationSeverityInfo, ObservationSeverityLow, ObservationSeverityMedium,
		ObservationSeverityHigh, ObservationSeverityCritical:
		return true
	default:
		return false
	}
}

// SecurityObservation is one security-relevant fact tied to a Configuration
// Item (E8.1a): the foundation of the SOC epic (E8), and the fact ingestion
// layer SOC detection (E8.1b, a later story) will read. It is deliberately
// NOT a reified SecurityEvent/Alert/Finding entity — the same discipline
// Sample's doc comment states for telemetry: a SecurityObservation is only
// the fact "this asset was observed exhibiting this kind of thing, from this
// source, at this time" — no status, no lifecycle, no correlation. It shares
// telemetry_sample's category (an append-only fact) but not its table: a
// telemetry Sample is a numeric measurement, and a SecurityObservation is a
// categorical/attributed fact that does not fit that shape (ADR-TELEMETRY-001,
// ADR-SOC-001).
type SecurityObservation struct {
	TenantID string
	// AssetID names the Configuration Item this observation is about. It is
	// re-verified against the caller's own tenant-scoped connection before
	// being written — see SecurityObservationRepository.WriteObservations's
	// doc comment — for the same reason Sample.AssetID is (ADR-ASSET-001 §6,
	// ADR-TELEMETRY-001): PostgreSQL's foreign-key checks bypass row-level
	// security on the referenced table, so the foreign key alone cannot be
	// trusted to keep this cross-tenant-safe.
	AssetID string
	// ObservationType categorises WHAT was observed (e.g. "port_scan",
	// "malware_detected", "failed_login") — lower-case snake_case, bounded
	// like Sample.Metric, and for the same reasons.
	ObservationType string
	// Source names the producer that reported this observation (e.g.
	// "wazuh", "cloudtrail", "ids-1"). Open-but-bounded, the same split
	// CollectorCheck.Target draws.
	Source string
	// Severity is a closed, ordered classification of how serious this
	// observation is — see ObservationSeverity's doc comment.
	Severity ObservationSeverity
	// ObservedAt is when the fact was observed, not when it was ingested —
	// the same distinction Sample.Timestamp draws, and the hypertable's
	// partitioning column.
	ObservedAt time.Time
	// Attributes carries additional, open-ended context (e.g. {"actor":
	// "203.0.113.5", "target_port": "22"}) — bounded by
	// MaxObservationAttributeCount so one observation cannot become an
	// unbounded payload, the same role Sample.Labels plays. There is no
	// dedicated "actor" column: an attributed detail belongs here, keeping
	// the fact shape minimal (no reified noun per attribute).
	Attributes map[string]string
}

// Validate enforces the invariants the database also enforces, so a bad
// observation fails with a field-level reason before it reaches the store.
func (o *SecurityObservation) Validate() error {
	if strings.TrimSpace(o.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(o.AssetID) == "" {
		return newValidation("asset_id", "must not be empty")
	}
	obsType := strings.TrimSpace(o.ObservationType)
	if obsType == "" {
		return newValidation("observation_type", "must not be empty")
	}
	if len(obsType) > MaxObservationTypeLength {
		return newValidation("observation_type", "must be at most 100 characters")
	}
	if !observationTypePattern.MatchString(obsType) {
		return newValidation("observation_type", "must be lower-case snake_case, e.g. port_scan, malware_detected")
	}
	source := strings.TrimSpace(o.Source)
	if source == "" {
		return newValidation("source", "must not be empty")
	}
	if len(source) > MaxObservationSourceLength {
		return newValidation("source", "must be at most 200 characters")
	}
	if !o.Severity.Valid() {
		return newValidation("severity", "must be one of: info, low, medium, high, critical")
	}
	if o.ObservedAt.IsZero() {
		return newValidation("observed_at", "must not be empty")
	}
	if len(o.Attributes) > MaxObservationAttributeCount {
		return newValidation("attributes", "must carry at most 20 attributes")
	}
	for k, v := range o.Attributes {
		if strings.TrimSpace(k) == "" {
			return newValidation("attributes", "an attribute key must not be blank")
		}
		if len(k) > MaxObservationAttributeKeyLength {
			return newValidation("attributes", "an attribute key must be at most 100 characters")
		}
		if len(v) > MaxObservationAttributeValueLength {
			return newValidation("attributes", "an attribute value must be at most 200 characters")
		}
	}
	return nil
}

// NewSecurityObservation builds a validated SecurityObservation. tenantID is
// always taken from the bound connection's context, never from request data
// — the same rule domain.NewSample follows — so a caller cannot plant an
// observation inside another tenant's boundary.
func NewSecurityObservation(
	tenantID, assetID, observationType, source string, severity ObservationSeverity,
	observedAt time.Time, attributes map[string]string,
) (*SecurityObservation, error) {
	if attributes == nil {
		attributes = map[string]string{}
	}
	o := &SecurityObservation{
		TenantID:        strings.TrimSpace(tenantID),
		AssetID:         strings.TrimSpace(assetID),
		ObservationType: strings.TrimSpace(observationType),
		Source:          strings.TrimSpace(source),
		Severity:        severity,
		ObservedAt:      observedAt,
		Attributes:      attributes,
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return o, nil
}

// SecurityObservationWriteResult is the outcome of writing one
// SecurityObservation from a batch — always one per input observation, at
// that observation's input index, the same shape SampleWriteResult uses for
// telemetry: one bad observation (a validation failure, or an asset_id the
// tenant cannot see) must not abort the observations around it.
type SecurityObservationWriteResult struct {
	Accepted bool
	// Reason is empty when Accepted; otherwise the validation or ownership
	// failure that refused this observation.
	Reason string
}

// DefaultSecurityObservationQueryLimit and MaxSecurityObservationQueryLimit
// bound SecurityObservationRepository.QueryRange, mirroring
// DefaultTelemetryQueryLimit/MaxTelemetryQueryLimit.
const (
	DefaultSecurityObservationQueryLimit = 500
	MaxSecurityObservationQueryLimit     = 5000
)

// MaxSecurityObservationIngestBatch bounds one ingest call, mirroring
// MaxTelemetryIngestBatch.
const MaxSecurityObservationIngestBatch = 1000

// SecurityObservationRepository administers ingestion and range queries over
// security_observation: an append-only fact table tied to Configuration Items
// (E8.1a) — the foundation of the SOC epic (E8), reusing telemetry's patterns
// on a sibling table rather than telemetry_sample itself (ADR-TELEMETRY-001,
// ADR-SOC-001).
//
// security_observation is TENANT-OWNED: it is in TenantOwnedTables and
// carries row-level security, so this repository takes no tenant argument
// anywhere — the bound connection already is the boundary (ADR-TENANCY-002).
// It is backed by a TimescaleDB hypertable behind this interface, mirroring
// domain.TelemetryRepository exactly.
type SecurityObservationRepository interface {
	// WriteObservations validates and writes a batch, returning one
	// SecurityObservationWriteResult per input observation, in input order —
	// see SecurityObservationWriteResult's doc comment. AssetID is
	// re-verified against this store's own tenant-scoped connection before
	// any row naming it is written: an asset_id the caller's tenant cannot
	// see (because it does not exist, or belongs to another tenant) is
	// reported as a rejected observation, not as a row naming a cross-tenant
	// asset (ADR-ASSET-001 §6, ADR-TELEMETRY-001, extended here). Re-writing
	// an identical (tenant, asset, observation_type, source, observed_at)
	// observation is idempotent: it is accepted again and creates no second
	// row — but idempotent is not the same as "merged", the same limitation
	// domain.TelemetryRepository.WriteSamples documents: the FIRST write for
	// that key wins permanently. Batch size is bounded by the transport
	// layer (MaxSecurityObservationIngestBatch); this method itself imposes
	// no additional cap.
	WriteObservations(ctx context.Context, observations []SecurityObservation) ([]SecurityObservationWriteResult, error)

	// QueryRange returns a bounded, keyset-paginated page of one asset's one
	// observation type, ordered by ObservedAt ascending, within [from, to].
	// after is the ObservedAt of the last observation returned by the
	// previous page, exclusive; the zero time.Time means "from the start of
	// the window". limit is clamped to (0, MaxSecurityObservationQueryLimit]
	// the same way TelemetryRepository.QueryRange clamps its page size.
	//
	// after's timestamp-only keyset works ONLY because (tenant_id, asset_id,
	// observation_type, source, observed_at) is the table's actual
	// uniqueness constraint (uq_security_observation) and this call already
	// fixes asset_id/observation_type — see
	// domain.TelemetryRepository.QueryRange's doc comment for the identical
	// caveat about widening the natural key.
	QueryRange(ctx context.Context, assetID, observationType string, from, to time.Time, limit int, after time.Time) ([]SecurityObservation, error)

	// QueryRangeForTenant is QueryRange's privileged-pool counterpart,
	// mirroring TelemetryRepository.QueryRangeForTenant: QueryRange's own
	// isolation is enforced by row-level security on this store's
	// tenant-scoped connection and carries no tenant predicate of its own.
	// A privileged, cross-tenant caller (a future SOC detection worker,
	// E8.1b) must supply tenantID as an explicit SQL predicate rather than
	// an ambient assumption — the same posture ADR-TENANCY-012 requires.
	// Returns observations ordered by ObservedAt ascending, bounded by
	// MaxSecurityObservationQueryLimit.
	QueryRangeForTenant(ctx context.Context, tenantID, assetID, observationType string, from, to time.Time) ([]SecurityObservation, error)

	// CountForTenant is the detector's (internal/security, E8.1b-2) own
	// privileged-pool read: how many of tenantID's assetID/observationType
	// observations, at or above minSeverity, fall within [from, to]. It is a
	// bounded SQL COUNT(*), never a List — a security_rule's window can span
	// up to MaxSecurityRuleWindowSeconds (24h) of a hypertable that may hold
	// far more rows than a detector tick should ever pull into Go, the same
	// "count, don't fetch" discipline AlertRuleStore.CountFiring applies to
	// alert_rule (E7.1). tenantID is an explicit SQL predicate rather than an
	// ambient connection property, exactly like QueryRangeForTenant's own —
	// this method exists FOR a privileged, cross-tenant caller (ADR-TENANCY-012).
	CountForTenant(ctx context.Context, tenantID, assetID, observationType string, minSeverity ObservationSeverity, from, to time.Time) (int, error)
}
