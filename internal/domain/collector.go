package domain

import (
	"context"
	"strings"
	"time"
)

// CollectorCheckTypeHTTP was the only check type E2.2a executed. Type itself
// is open-but-validated, exactly like Asset.Type (ADR-ASSET-001 §1): new
// values add to the same open set when they land, not a migration widening a
// closed enum. A CollectorCheck of an unrecognised type is stored without
// error — the scheduler simply has no executor for it yet, and records
// CollectorCheckStatusUnsupported rather than crashing or silently never
// running it (see collector.Scheduler).
const CollectorCheckTypeHTTP = "http"

// CollectorCheckTypeSNMP is the network/device layer's first check type
// (Phase A.1, ADR-NET-001): SNMP v2c reachability. Target is "host" or
// "host:port" (port defaults to 161 when omitted — see
// collector.splitSNMPTarget); SNMPCommunity is REQUIRED for this type (see
// its own doc comment) and forbidden for every other type. Bounded to
// reachability + uptime (a GET of sysUpTime.0): richer OIDs (interfaces,
// CPU, memory — A.1b) and network discovery (A.2) build on this without
// changing it. v3 (which needs no shared-secret community string at all) is
// a deliberate follow-up, not solved here — see SNMPCommunity's doc comment
// for why that follow-up matters.
const CollectorCheckTypeSNMP = "snmp"

// collectorCheckTypePattern reuses assetTypePattern: the same lower-case
// snake_case discipline, for the same reason (fit to appear in a filter, a
// metric label, or a route).
var collectorCheckTypePattern = assetTypePattern

// MaxCollectorCheckTypeLength bounds Type, mirroring MaxAssetTypeLength.
const MaxCollectorCheckTypeLength = 100

// MaxCollectorCheckTargetLength bounds Target. Unlike Type this carries no
// format constraint at the domain level — what a target string means depends
// on Type (a URL for "http"; a "host" or "host:port" for "snmp", the
// community travelling separately in SNMPCommunity; ADR-ASSET-001 §1's shape
// of "open but validated only on what every type shares") — so only a
// generous length bound applies here. The type-specific format check (e.g.
// safehttp.ValidateWebhookURL for "http") happens at the transport boundary,
// exactly where createWebhook checks its own URL. For "http" the
// authoritative SSRF egress guard additionally runs at dial time in safehttp
// (ADR-SECURITY-001); "snmp" dials UDP:161 to an operator-configured device
// address, not a URL, so that HTTP-specific guard class does not apply to it
// (ADR-NET-001).
const MaxCollectorCheckTargetLength = 500

// MaxSNMPCommunityLength bounds SNMPCommunity. Real-world community strings
// are short (often "public"/"private" or a handful of characters); this is a
// generous ceiling, not a realistic expectation.
const MaxSNMPCommunityLength = 255

// MaxCollectorMetricPrefixLength bounds MetricPrefix. It is deliberately
// tighter than MaxTelemetryMetricLength (100): the executor appends a suffix
// ("_up", "_latency_ms", "_status_code" — the longest is 11 bytes) to build
// each Sample.Metric, and domain.Sample.Validate rejects a metric over 100
// characters, so the prefix alone must leave room for the widest suffix.
const MaxCollectorMetricPrefixLength = 80

// MinCollectorCheckIntervalSeconds and MaxCollectorCheckIntervalSeconds bound
// how often a check may poll its target. The floor exists so a misconfigured
// check cannot hammer a customer-supplied target (or the platform's own
// scheduler) every second; the ceiling exists so a check is never configured
// so infrequently it stops being a monitoring signal at all.
const (
	MinCollectorCheckIntervalSeconds = 30
	MaxCollectorCheckIntervalSeconds = 30 * 24 * 3600 // 30 days
)

// collectorMetricPrefixPattern is the same lower-case snake_case discipline
// telemetryMetricPattern applies to Sample.Metric, applied here to the
// prefix half of it.
var collectorMetricPrefixPattern = telemetryMetricPattern

// CollectorCheckStatus is the outcome CollectorCheck.LastStatus records after
// the scheduler last ran a check. It is a scheduler-derived fact, not a
// caller-settable field — see CollectorCheckPatch's doc comment.
type CollectorCheckStatus string

const (
	// CollectorCheckStatusOK: the check's last run got a complete response
	// from its target (any HTTP status code, for an "http" check — see
	// collector.Scheduler's doc comment on what "up" means).
	CollectorCheckStatusOK CollectorCheckStatus = "ok"
	// CollectorCheckStatusDown: the check's last run failed or timed out —
	// no response was received within the bounded per-check timeout. This is
	// a recorded fact, not a dropped sample: an "up=0" telemetry sample is
	// still written (see collector.Scheduler).
	CollectorCheckStatusDown CollectorCheckStatus = "down"
	// CollectorCheckStatusUnsupported: the check's Type has no executor in
	// this build (e.g. a future cloud/API poller type created ahead of its
	// executor landing). The scheduler still records a run so the due-scan
	// does not re-select it every pass, but writes no telemetry sample —
	// there is no result to report.
	CollectorCheckStatusUnsupported CollectorCheckStatus = "unsupported"
)

// Valid reports whether s is a status the scheduler may record.
func (s CollectorCheckStatus) Valid() bool {
	switch s {
	case CollectorCheckStatusOK, CollectorCheckStatusDown, CollectorCheckStatusUnsupported:
		return true
	default:
		return false
	}
}

// CollectorCheck is a configured target the agentless collector framework
// polls on an interval, writing its result as telemetry samples tied to the
// Configuration Item it checks (E2.2a). It is deliberately not a reified
// Alert/Metric — see Sample's doc comment for the same reasoning applied one
// layer up: a CollectorCheck only describes WHAT to poll and HOW OFTEN; the
// poll's result is a Sample like any other producer's.
type CollectorCheck struct {
	CheckID  string
	TenantID string
	// AssetID names the Configuration Item this check's results describe. It
	// is re-verified against the caller's own tenant-scoped connection before
	// being written — the same defense AssetRepository.CreateRelationship and
	// TelemetryRepository.WriteSamples apply (ADR-ASSET-001 §6) — because the
	// foreign key alone bypasses row-level security on asset.
	AssetID string
	// Type selects the executor: CollectorCheckTypeHTTP or
	// CollectorCheckTypeSNMP today. Open-but-validated — see
	// CollectorCheckTypeHTTP's doc comment.
	Type string
	// Target is interpreted according to Type — a URL for "http", a "host"
	// or "host:port" for "snmp". See MaxCollectorCheckTargetLength's doc
	// comment for why no format constraint lives here.
	Target          string
	IntervalSeconds int
	Enabled         bool
	// MetricPrefix names the family of telemetry metrics this check produces
	// — e.g. "checkout_api" yields checkout_api_up, checkout_api_latency_ms,
	// checkout_api_status_code (collector.Scheduler builds these).
	MetricPrefix string
	// SNMPCommunity is the SNMP v2c community string — a device credential —
	// REQUIRED (non-empty) when Type is CollectorCheckTypeSNMP and forbidden
	// (must be empty) for every other type (Validate enforces both
	// directions). There is no secret store anywhere in this platform yet, so
	// this column is protected the only way currently available: RLS-scoped
	// like every other column on this tenant-owned row (ADR-TENANCY-001), and
	// REDACTED from every read — httpapi's collectorCheckDTO never
	// serialises it, only a has_snmp_community boolean, and it is settable
	// only through the request bodies that write it, never returned by any
	// response. At-rest encryption of this column and SNMP v3 (which
	// authenticates per-user instead of a shared secret, so it would not
	// need this field at all) are flagged, deliberate follow-ups — not
	// solved by this increment.
	SNMPCommunity string
	// LastRunAt/LastStatus are set only by the scheduler (collector.Scheduler
	// via CollectorCheckRepository's background counterpart), never by a
	// caller through Create/Update — the same split Asset.LastSeen draws:
	// a background confirmation is not a field a caller is patching. Nil/""
	// means "never run".
	LastRunAt  *time.Time
	LastStatus CollectorCheckStatus
	RowVersion int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Validate enforces the invariants the database also enforces, so a bad
// check fails with a field-level reason before it reaches the store.
func (c *CollectorCheck) Validate() error {
	if strings.TrimSpace(c.CheckID) == "" {
		return newValidation("check_id", "must not be empty")
	}
	if strings.TrimSpace(c.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(c.AssetID) == "" {
		return newValidation("asset_id", "must not be empty")
	}
	checkType := strings.TrimSpace(c.Type)
	if checkType == "" {
		return newValidation("type", "must not be empty")
	}
	if len(checkType) > MaxCollectorCheckTypeLength {
		return newValidation("type", "must be at most 100 characters")
	}
	if !collectorCheckTypePattern.MatchString(checkType) {
		return newValidation("type", "must be lower-case snake_case, e.g. http")
	}
	target := strings.TrimSpace(c.Target)
	if target == "" {
		return newValidation("target", "must not be empty")
	}
	if len(target) > MaxCollectorCheckTargetLength {
		return newValidation("target", "must be at most 500 characters")
	}
	if c.IntervalSeconds < MinCollectorCheckIntervalSeconds || c.IntervalSeconds > MaxCollectorCheckIntervalSeconds {
		return newValidation("interval_seconds", "must be between 30 and 2592000 (30 days)")
	}
	prefix := strings.TrimSpace(c.MetricPrefix)
	if prefix == "" {
		return newValidation("metric_prefix", "must not be empty")
	}
	if len(prefix) > MaxCollectorMetricPrefixLength {
		return newValidation("metric_prefix", "must be at most 80 characters")
	}
	if !collectorMetricPrefixPattern.MatchString(prefix) {
		return newValidation("metric_prefix", "must be lower-case snake_case, e.g. checkout_api")
	}
	if c.LastStatus != "" && !c.LastStatus.Valid() {
		return newValidation("last_status", "must be one of: ok, down, unsupported")
	}
	community := strings.TrimSpace(c.SNMPCommunity)
	switch checkType {
	case CollectorCheckTypeSNMP:
		if community == "" {
			return newValidation("snmp_community", "must not be empty when type is snmp")
		}
		if len(community) > MaxSNMPCommunityLength {
			return newValidation("snmp_community", "must be at most 255 characters")
		}
	default:
		if community != "" {
			return newValidation("snmp_community", "must be empty unless type is snmp")
		}
	}
	return nil
}

// NewCollectorCheck builds a validated, enabled CollectorCheck. tenantID is
// always taken from the bound connection's context, never from request data,
// the same rule NewAsset and NewSample follow. snmpCommunity is ignored
// (should be passed "") for every type except CollectorCheckTypeSNMP, which
// requires it non-empty — see SNMPCommunity's doc comment.
func NewCollectorCheck(tenantID, assetID, checkType, target string, intervalSeconds int, metricPrefix, snmpCommunity string) (*CollectorCheck, error) {
	c := &CollectorCheck{
		CheckID:         NewID(),
		TenantID:        strings.TrimSpace(tenantID),
		AssetID:         strings.TrimSpace(assetID),
		Type:            strings.TrimSpace(checkType),
		Target:          strings.TrimSpace(target),
		IntervalSeconds: intervalSeconds,
		Enabled:         true,
		MetricPrefix:    strings.TrimSpace(metricPrefix),
		SNMPCommunity:   strings.TrimSpace(snmpCommunity),
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// CollectorCheckPatch carries the fields CollectorCheckRepository.Update may
// change; a nil pointer leaves that field unchanged. AssetID is deliberately
// absent — like AssetRelationship's endpoints, which CI a check watches is
// fixed at creation, not a field to repoint later; delete and recreate
// instead. LastRunAt/LastStatus are likewise absent: only the scheduler's
// background counterpart (CollectorCheckRepository's RecordRun-shaped
// surface) sets them — see CollectorCheck's doc comment. SNMPCommunity IS
// patchable — unlike a webhook secret there is no way to prove possession of
// the current one before rotating it, but it is still write-only: applying
// it never returns the value (Update's caller only ever sees the redacted
// DTO), so this is the credential's "dedicated set" path, reusing the
// existing optimistic-locked PATCH rather than adding a second endpoint.
type CollectorCheckPatch struct {
	Type            *string
	Target          *string
	IntervalSeconds *int
	Enabled         *bool
	MetricPrefix    *string
	SNMPCommunity   *string
}

// CollectorCheckRepository administers collector_check: the agentless
// collector framework's target configuration (E2.2a).
//
// collector_check is TENANT-OWNED: it is in TenantOwnedTables and carries
// row-level security, so this repository takes no tenant argument anywhere —
// the bound connection already is the boundary (ADR-TENANCY-002), exactly
// like AssetRepository.
type CollectorCheckRepository interface {
	// Create inserts a check. AssetID must already be visible to the
	// caller's tenant — re-verified against this store's own tenant-scoped
	// connection before the row is written (ADR-ASSET-001 §6, extended
	// here); an asset_id the tenant cannot see returns ErrNotFound.
	Create(ctx context.Context, c *CollectorCheck) (*CollectorCheck, error)
	Get(ctx context.Context, checkID string) (*CollectorCheck, error)
	// List returns a page of the caller's checks, keyset-paginated over
	// check_id.
	List(ctx context.Context, limit int, after string) ([]*CollectorCheck, error)
	// Update changes one or more fields under optimistic locking. Returns
	// ErrVersionMismatch on a stale rowVersion, ErrNotFound if checkID does
	// not name a check visible to this tenant.
	Update(ctx context.Context, checkID string, rowVersion int64, patch CollectorCheckPatch) (*CollectorCheck, error)
	// Delete removes a check, or returns ErrNotFound.
	Delete(ctx context.Context, checkID string) error
}
