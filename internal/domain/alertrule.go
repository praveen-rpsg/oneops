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

// AlertSymptomClass classifies WHAT a rule detects, not how severe it is
// (that is AlertSeverity) — a taxonomy future consumers act on (E3.4). It is
// deliberately minimal: the one distinction dependency suppression (E3.3b)
// and root-cause ranking (E4.2) actually need is whether the symptom cascades
// through dependencies. availability = reachability/up-ness (it cascades: a
// downstream CI looks unreachable because its dependency is down); resource =
// a resource/utilization metric such as cpu/disk/mem/latency (it is usually
// independent of a dependency's own health). unspecified is the default for
// every rule an operator has not explicitly classified. E3.4 is the
// PRIMITIVE ONLY: this field is stored and exposed but nothing reads it to
// make a decision yet — see DefaultAlertSymptomClass's doc comment.
type AlertSymptomClass string

const (
	AlertSymptomClassAvailability AlertSymptomClass = "availability"
	AlertSymptomClassResource     AlertSymptomClass = "resource"
	AlertSymptomClassUnspecified  AlertSymptomClass = "unspecified"
)

// Valid reports whether c is a defined symptom class.
func (c AlertSymptomClass) Valid() bool {
	switch c {
	case AlertSymptomClassAvailability, AlertSymptomClassResource, AlertSymptomClassUnspecified:
		return true
	default:
		return false
	}
}

// DefaultAlertSymptomClass is what a rule gets when an operator does not
// explicitly classify it, on create and via the migration backfill of every
// pre-existing row. It is NEVER inferred from Metric — a name-based guess is
// fragile and would silently mis-suppress or mis-rank once a future story
// (class-scoped E3.3b suppression, class-aware E4.2 root-cause ranking)
// starts consuming this field — so an unclassified rule stays unspecified,
// and unspecified is deliberately excluded from any future
// dependency/root-cause behaviour those stories add, keeping every rule that
// predates E3.4 behaving EXACTLY as it did before this field existed.
const DefaultAlertSymptomClass = AlertSymptomClassUnspecified

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

// MinAlertRuleFlapDwellSeconds, MaxAlertRuleFlapDwellSeconds and
// DefaultAlertRuleFlapDwellSeconds bound and default FlapDwellSeconds (E3.2):
// how long a candidate transition must be observed continuously before the
// evaluator commits it — see FlapDwellSeconds's own doc comment. The floor is
// 0, not 1 like ForDuration's: 0 is a deliberate, per-rule opt-out that
// reproduces E3.1's original immediate-transition behaviour (a rule that
// genuinely needs zero-latency transitions, e.g. a hand-tuned rule an
// operator has already verified does not flap), not an accidental bypass —
// it still requires an explicit per-rule choice, never a global switch. The
// ceiling matches ForDuration's for the same reason: a dwell an operator
// cannot reason about is not a safety margin, it is a missed alert. The
// default (60s) is short enough not to meaningfully delay a genuinely
// sustained transition yet long enough to absorb the common flapping period
// (a metric oscillating faster than it takes an operator to react).
const (
	MinAlertRuleFlapDwellSeconds     = 0
	MaxAlertRuleFlapDwellSeconds     = 24 * 3600
	DefaultAlertRuleFlapDwellSeconds = 60
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
	// SymptomClass is E3.4's taxonomy primitive — see AlertSymptomClass's own
	// doc comment. Explicit and operator-set, defaulting to
	// DefaultAlertSymptomClass; unlike LastState/PendingState it IS patchable
	// through the row_version-guarded Update path, the same config-field
	// treatment FlapDwellSeconds gets.
	SymptomClass AlertSymptomClass
	Enabled      bool
	// LastState/LastTransitionAt are set only by the evaluator
	// (internal/alerting.Evaluator via AlertRuleRepository's background
	// counterpart), never through the row_version-guarded Update path — see
	// AlertRulePatch's doc comment. A new rule starts at AlertRuleStateOK
	// with a zero LastTransitionAt ("never evaluated").
	LastState        AlertRuleState
	LastTransitionAt time.Time
	// FlapDwellSeconds is E3.2's de-flap/hysteresis window: a candidate state
	// that differs from LastState must be observed continuously, tick over
	// tick, for this many seconds before the evaluator commits it as a real
	// transition (notify + E4.1 correlate + RecordTransition). A candidate
	// that reverts before the dwell elapses resets the clock instead of
	// committing, so a metric oscillating around its threshold produces at
	// most one eventual transition per stable dwell rather than one per
	// crossing (ADR-ALERTING-001). It is distinct from ForDuration: ForDuration
	// decides whether a breach counts as the candidate AT ALL (ok vs firing,
	// evaluated fresh every tick from raw telemetry); FlapDwellSeconds decides
	// whether a candidate that already differs from the committed state has
	// been stable long enough to ACT on. Defaults to
	// DefaultAlertRuleFlapDwellSeconds; 0 disables the dwell for this rule
	// (see that constant's doc comment).
	FlapDwellSeconds int
	// PendingState/PendingSince are the dwell's own in-flight bookkeeping: the
	// not-yet-committed candidate (if any) that currently differs from
	// LastState, and when the evaluator first observed it continuously.
	// PendingState=="" (and PendingSince zero) means nothing is pending — the
	// last-observed candidate agreed with LastState, the common case for a
	// rule that is not flapping. Set only by the evaluator's background write
	// path (alerting.Store.RecordPending), never through the row_version-
	// guarded Update path — the same split AlertRulePatch's doc comment
	// describes for LastState/LastTransitionAt/CurrentIncidentID — and reset
	// to ""/zero by RecordTransition (a committed transition supersedes
	// whatever was pending) and by every admin Update (a dwell counted
	// against the rule's OLD config must not be silently honoured under a
	// new one — see AlertRuleRepository.Update's doc comment).
	PendingState AlertRuleState
	PendingSince time.Time
	// CurrentIncidentID is the open, alert-sourced Incident this rule's
	// firing is currently linked to, or nil when the rule is ok (E4.1). Like
	// LastState/LastTransitionAt, it is set only by the evaluator's
	// background write path (alerting.Store.RecordTransition), never through
	// the row_version-guarded Update path — see AlertRulePatch's doc
	// comment. Cleared (set back to nil) on the matching firing->ok
	// transition; the linked Incident itself is never auto-resolved or
	// auto-closed by that clearing (E4.1's scope: correlate, don't remediate).
	CurrentIncidentID *string
	RowVersion        int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
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
	if !r.SymptomClass.Valid() {
		return newValidation("symptom_class", "must be one of: availability, resource, unspecified")
	}
	if !r.LastState.Valid() {
		return newValidation("last_state", "must be one of: ok, firing")
	}
	if r.FlapDwellSeconds < MinAlertRuleFlapDwellSeconds || r.FlapDwellSeconds > MaxAlertRuleFlapDwellSeconds {
		return newValidation("flap_dwell_seconds", "must be between 0 and 86400 (24 hours)")
	}
	if r.PendingState != "" && !r.PendingState.Valid() {
		return newValidation("pending_state", "must be empty, ok, or firing")
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
		RuleID:           NewID(),
		TenantID:         strings.TrimSpace(tenantID),
		AssetID:          strings.TrimSpace(assetID),
		Metric:           strings.TrimSpace(metric),
		Comparator:       comparator,
		Threshold:        threshold,
		ForDuration:      forDurationSeconds,
		Severity:         severity,
		SymptomClass:     DefaultAlertSymptomClass,
		Enabled:          true,
		LastState:        AlertRuleStateOK,
		FlapDwellSeconds: DefaultAlertRuleFlapDwellSeconds,
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
// recreate instead. LastState/LastTransitionAt/CurrentIncidentID/
// PendingState/PendingSince are likewise absent: only the evaluator's
// background counterpart sets them — see AlertRule's doc comment.
//
// Applying ANY patch — including one that only touches FlapDwellSeconds
// itself — clears a rule's in-flight PendingState/PendingSince (E3.2): a
// dwell measured against the rule's OLD comparator/threshold/dwell
// configuration must not be silently honoured under a new one. The next
// evaluation tick starts a fresh dwell measurement under whatever config now
// applies.
type AlertRulePatch struct {
	Comparator       *AlertComparator
	Threshold        *float64
	ForDuration      *int
	Severity         *AlertSeverity
	SymptomClass     *AlertSymptomClass
	Enabled          *bool
	FlapDwellSeconds *int
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
	// Update changes one or more fields under optimistic locking, and clears
	// any in-flight PendingState/PendingSince (E3.2 — see AlertRulePatch's doc
	// comment) as part of the same write. Returns ErrVersionMismatch on a
	// stale rowVersion, ErrNotFound if ruleID does not name a rule visible to
	// this tenant.
	Update(ctx context.Context, ruleID string, rowVersion int64, patch AlertRulePatch) (*AlertRule, error)
	// Delete removes a rule, or returns ErrNotFound.
	Delete(ctx context.Context, ruleID string) error
}
