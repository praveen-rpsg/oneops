package domain

import (
	"context"
	"math"
	"strings"
	"time"
)

// AlertComparator is the relational operator an AlertRule evaluates its
// metric's value against Threshold.
type AlertComparator string

const (
	ComparatorGT  AlertComparator = "gt"
	ComparatorLT  AlertComparator = "lt"
	ComparatorGTE AlertComparator = "gte"
	ComparatorLTE AlertComparator = "lte"
)

// Valid reports whether c is a defined comparator.
func (c AlertComparator) Valid() bool {
	switch c {
	case ComparatorGT, ComparatorLT, ComparatorGTE, ComparatorLTE:
		return true
	default:
		return false
	}
}

// Breached reports whether value crosses threshold under this comparator.
func (c AlertComparator) Breached(value, threshold float64) bool {
	switch c {
	case ComparatorGT:
		return value > threshold
	case ComparatorLT:
		return value < threshold
	case ComparatorGTE:
		return value >= threshold
	case ComparatorLTE:
		return value <= threshold
	default:
		return false
	}
}

// AlertSeverity is a label carried on an AlertRule and forwarded onto the
// Notification a firing produces. It changes nothing about evaluation: E3.1
// derives exactly one kind of consequence (a Notification) regardless of
// severity — routing/paging by severity is E5.2 (on-call & escalation).
type AlertSeverity string

const (
	AlertSeverityCritical AlertSeverity = "critical"
	AlertSeverityWarning  AlertSeverity = "warning"
	AlertSeverityInfo     AlertSeverity = "info"
)

// Valid reports whether s is a defined severity.
func (s AlertSeverity) Valid() bool {
	switch s {
	case AlertSeverityCritical, AlertSeverityWarning, AlertSeverityInfo:
		return true
	default:
		return false
	}
}

// AlertRuleState is the firing state a rule's evaluation last settled on.
// It is a derived fact the evaluator writes, not a caller-settable field —
// see AlertRulePatch's doc comment, the same split CollectorCheck draws for
// LastRunAt/LastStatus.
type AlertRuleState string

const (
	AlertRuleStateOK     AlertRuleState = "ok"
	AlertRuleStateFiring AlertRuleState = "firing"
)

// Valid reports whether s is a defined firing state.
func (s AlertRuleState) Valid() bool {
	return s == AlertRuleStateOK || s == AlertRuleStateFiring
}

// MinAlertRuleForDurationSeconds and MaxAlertRuleForDurationSeconds bound how
// long a breach must be sustained before a rule fires. The floor rules out a
// zero/negative window, which would fire on a single sample and defeat the
// "sustained" half of "sustained breach". The ceiling (24h) keeps the
// evaluator's per-tick telemetry read (E3.1 reads raw samples only) inside a
// window an operator can reason about; a rule wanting to react to a
// multi-day trend is a rate-of-change/anomaly rule type (deferred, E3.1's
// scope is threshold only).
const (
	MinAlertRuleForDurationSeconds = 1
	MaxAlertRuleForDurationSeconds = 24 * 3600
)

// AlertRule is a threshold config over one asset's one metric — a firing is
// DERIVED by evaluating it against telemetry (E3.1), never a reified
// Alert/Event/Signal entity (docs/PLATFORM-BUILD-PLAN.md §4, Vol II §5.3):
// this row only describes WHEN to consider the metric breached and for how
// long, the same split CollectorCheck draws for what to poll (Sample's doc
// comment) applied one layer up.
type AlertRule struct {
	RuleID   string
	TenantID string
	// AssetID names the Configuration Item this rule watches. It is
	// re-verified against the caller's own tenant-scoped connection before
	// being written — the same defense TelemetryRepository.WriteSamples and
	// CollectorCheckRepository.Create apply (ADR-ASSET-001 §6) — because the
	// foreign key alone bypasses row-level security on asset.
	AssetID string
	// Metric names the Sample.Metric this rule watches, the same lower-case
	// snake_case discipline telemetry samples themselves carry.
	Metric      string
	Comparator  AlertComparator
	Threshold   float64
	ForDuration int // seconds; see MinAlertRuleForDurationSeconds/Max's doc comment
	Severity    AlertSeverity
	Enabled     bool
	// LastState/LastTransitionAt are set only by the evaluator
	// (internal/alerting.Evaluator via AlertRuleRepository's background
	// counterpart), never through the row_version-guarded Update path — see
	// AlertRulePatch's doc comment. A new rule starts at AlertRuleStateOK
	// with a zero LastTransitionAt ("never evaluated").
	LastState        AlertRuleState
	LastTransitionAt time.Time
	RowVersion       int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Validate enforces the invariants the database also enforces, so a bad rule
// fails with a field-level reason before it reaches the store.
func (r *AlertRule) Validate() error {
	if strings.TrimSpace(r.RuleID) == "" {
		return newValidation("rule_id", "must not be empty")
	}
	if strings.TrimSpace(r.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(r.AssetID) == "" {
		return newValidation("asset_id", "must not be empty")
	}
	metric := strings.TrimSpace(r.Metric)
	if metric == "" {
		return newValidation("metric", "must not be empty")
	}
	if len(metric) > MaxTelemetryMetricLength {
		return newValidation("metric", "must be at most 100 characters")
	}
	if !telemetryMetricPattern.MatchString(metric) {
		return newValidation("metric", "must be lower-case snake_case, e.g. cpu_utilization, disk_free_bytes")
	}
	if !r.Comparator.Valid() {
		return newValidation("comparator", "must be one of: gt, lt, gte, lte")
	}
	if math.IsNaN(r.Threshold) || math.IsInf(r.Threshold, 0) {
		return newValidation("threshold", "must be a finite number")
	}
	if r.ForDuration < MinAlertRuleForDurationSeconds || r.ForDuration > MaxAlertRuleForDurationSeconds {
		return newValidation("for_duration_seconds", "must be between 1 and 86400 (24 hours)")
	}
	if !r.Severity.Valid() {
		return newValidation("severity", "must be one of: critical, warning, info")
	}
	if !r.LastState.Valid() {
		return newValidation("last_state", "must be one of: ok, firing")
	}
	return nil
}

// NewAlertRule builds a validated, enabled AlertRule, freshly minted and
// never yet evaluated (LastState=ok, LastTransitionAt zero). tenantID is
// always taken from the bound connection's context, never from request data,
// the same rule NewAsset and NewCollectorCheck follow.
func NewAlertRule(
	tenantID, assetID, metric string, comparator AlertComparator, threshold float64,
	forDurationSeconds int, severity AlertSeverity,
) (*AlertRule, error) {
	r := &AlertRule{
		RuleID:      NewID(),
		TenantID:    strings.TrimSpace(tenantID),
		AssetID:     strings.TrimSpace(assetID),
		Metric:      strings.TrimSpace(metric),
		Comparator:  comparator,
		Threshold:   threshold,
		ForDuration: forDurationSeconds,
		Severity:    severity,
		Enabled:     true,
		LastState:   AlertRuleStateOK,
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// AlertRulePatch carries the fields AlertRuleRepository.Update may change; a
// nil pointer leaves that field unchanged. AssetID and Metric are
// deliberately absent — like CollectorCheckPatch's AssetID — what a rule
// watches is fixed at creation, not a field to repoint later: delete and
// recreate instead. LastState/LastTransitionAt are likewise absent: only the
// evaluator's background counterpart sets them — see AlertRule's doc comment.
type AlertRulePatch struct {
	Comparator  *AlertComparator
	Threshold   *float64
	ForDuration *int
	Severity    *AlertSeverity
	Enabled     *bool
}

// AlertRuleRepository administers alert_rule: E3.1's threshold config over
// telemetry.
//
// alert_rule is TENANT-OWNED: it is in TenantOwnedTables and carries
// row-level security, so this repository takes no tenant argument anywhere —
// the bound connection already is the boundary (ADR-TENANCY-002), exactly
// like CollectorCheckRepository.
type AlertRuleRepository interface {
	// Create inserts a rule. AssetID must already be visible to the caller's
	// tenant — re-verified against this store's own tenant-scoped connection
	// before the row is written (ADR-ASSET-001 §6, extended here); an
	// asset_id the tenant cannot see returns ErrNotFound.
	Create(ctx context.Context, r *AlertRule) (*AlertRule, error)
	Get(ctx context.Context, ruleID string) (*AlertRule, error)
	// List returns a page of the caller's rules, keyset-paginated over
	// rule_id.
	List(ctx context.Context, limit int, after string) ([]*AlertRule, error)
	// Update changes one or more fields under optimistic locking. Returns
	// ErrVersionMismatch on a stale rowVersion, ErrNotFound if ruleID does
	// not name a rule visible to this tenant.
	Update(ctx context.Context, ruleID string, rowVersion int64, patch AlertRulePatch) (*AlertRule, error)
	// Delete removes a rule, or returns ErrNotFound.
	Delete(ctx context.Context, ruleID string) error
}
