package domain

import (
	"context"
	"strings"
	"time"
)

// MinSecurityRuleWindowSeconds and MaxSecurityRuleWindowSeconds bound
// WindowSeconds: the trailing lookback a rule counts security_observation
// rows over. The floor rules out a zero/negative window, which would count
// nothing; the ceiling (24h) mirrors MinAlertRuleForDurationSeconds/
// MaxAlertRuleForDurationSeconds for the identical reason — a window an
// operator cannot reason about is not a detection rule, it is a full-table
// scan waiting to happen (E8.1b-2's evaluator, not this story, will bound its
// own query by this same window).
const (
	MinSecurityRuleWindowSeconds = 1
	MaxSecurityRuleWindowSeconds = 24 * 3600
)

// MinSecurityRuleThresholdCount and MaxSecurityRuleThresholdCount bound
// ThresholdCount. The floor (1) rules out a rule that fires on zero matching
// observations, which is not a threshold at all. The ceiling keeps the value
// meaningful — a threshold no realistic observation volume could ever reach
// is a configuration mistake, not a rule — the same "bounded, not
// unbounded" discipline MaxObservationAttributeCount and
// MaxSecurityObservationIngestBatch apply to this epic's other primitives.
const (
	MinSecurityRuleThresholdCount = 1
	MaxSecurityRuleThresholdCount = 1_000_000
)

// SecurityRuleState is the firing state a rule's evaluation last settled on.
// It is a derived fact the detector (E8.1b-2) writes, not a caller-settable
// field — see SecurityRulePatch's doc comment, the same split AlertRulePatch
// draws for AlertRule.LastState. Deliberately its own type rather than a
// reuse of AlertRuleState: alert_rule and security_rule are evaluated by
// separate subsystems (internal/alerting vs. the future SOC detector) and
// nothing should couple their state vocabularies just because the two
// currently happen to share the same two values.
type SecurityRuleState string

const (
	SecurityRuleStateOK     SecurityRuleState = "ok"
	SecurityRuleStateFiring SecurityRuleState = "firing"
)

// Valid reports whether s is a defined firing state.
func (s SecurityRuleState) Valid() bool {
	return s == SecurityRuleStateOK || s == SecurityRuleStateFiring
}

// DefaultSecurityRuleMinSeverity is what a rule gets when an operator does
// not explicitly set MinSeverity — the least restrictive severity, so an
// unclassified rule counts every observation of its ObservationType,
// mirroring DefaultAlertSymptomClass's "no silent narrowing of behaviour"
// discipline.
const DefaultSecurityRuleMinSeverity = ObservationSeverityInfo

// SecurityRule is a threshold-over-window SIEM detection config: it fires
// when the count of one asset's ObservationType observations, at or above
// MinSeverity, within a trailing WindowSeconds, meets ThresholdCount. Like
// AlertRule, a firing is DERIVED by evaluating this config against
// security_observation — the detector itself is E8.1b-2, a later, separate
// story; this row is config only (docs/PLATFORM-BUILD-PLAN.md §4, ADR-SOC-001,
// ADR-SOC-002). It is deliberately NOT a reified Alert/Detection/Correlation
// entity, the same discipline AlertRule's own doc comment states.
type SecurityRule struct {
	RuleID   string
	TenantID string
	// AssetID names the Configuration Item this rule watches. It is
	// re-verified against the caller's own tenant-scoped connection before
	// being written — the same defense AlertRule.AssetID's doc comment
	// describes (ADR-ASSET-001 §6).
	AssetID string
	// ObservationType names the SecurityObservation.ObservationType this rule
	// counts — the same lower-case snake_case discipline and length bound
	// SecurityObservation.ObservationType itself carries (E8.1a).
	ObservationType string
	// MinSeverity counts only observations at or above this severity
	// (evaluator-owned comparison, E8.1b-2 — nothing compares it yet).
	// Defaults to DefaultSecurityRuleMinSeverity when not set explicitly;
	// unlike LastState/CurrentIncidentID it IS patchable through the
	// row_version-guarded Update path, the same config-field treatment
	// AlertRule.SymptomClass gets.
	MinSeverity ObservationSeverity
	// WindowSeconds is the trailing lookback the count is taken over — see
	// MinSecurityRuleWindowSeconds/MaxSecurityRuleWindowSeconds's doc comment.
	WindowSeconds int
	// ThresholdCount is the matching-observation count that must be met or
	// exceeded for this rule to fire — see MinSecurityRuleThresholdCount/
	// MaxSecurityRuleThresholdCount's doc comment.
	ThresholdCount int
	// IncidentSeverity is the severity of the Incident the detector will
	// raise on a firing (E8.1b-2) — the INCIDENT severity vocabulary
	// (IncidentSeverity: critical/high/medium/low), NOT ObservationSeverity:
	// the two are different closed sets for different concerns, the same
	// split AlertSeverity and ObservationSeverity already draw from each
	// other (ObservationSeverity's own doc comment).
	IncidentSeverity IncidentSeverity
	Enabled          bool
	// LastState is set only by the future detector's background write path
	// (E8.1b-2), never through the row_version-guarded Update path — see
	// SecurityRulePatch's doc comment. A new rule starts at
	// SecurityRuleStateOK. Nothing in this story writes it after creation.
	LastState SecurityRuleState
	// CurrentIncidentID is the open Incident this rule's firing is currently
	// linked to, or nil when the rule is ok — set only by the future
	// detector's background write path (E8.1b-2), never through Update. Like
	// LastState, nothing in this story writes it after creation.
	CurrentIncidentID *string
	RowVersion        int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// Validate enforces the invariants the database also enforces, so a bad rule
// fails with a field-level reason before it reaches the store.
func (r *SecurityRule) Validate() error {
	if strings.TrimSpace(r.RuleID) == "" {
		return newValidation("rule_id", "must not be empty")
	}
	if strings.TrimSpace(r.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	if strings.TrimSpace(r.AssetID) == "" {
		return newValidation("asset_id", "must not be empty")
	}
	obsType := strings.TrimSpace(r.ObservationType)
	if obsType == "" {
		return newValidation("observation_type", "must not be empty")
	}
	if len(obsType) > MaxObservationTypeLength {
		return newValidation("observation_type", "must be at most 100 characters")
	}
	if !observationTypePattern.MatchString(obsType) {
		return newValidation("observation_type", "must be lower-case snake_case, e.g. port_scan, malware_detected")
	}
	if !r.MinSeverity.Valid() {
		return newValidation("min_severity", "must be one of: info, low, medium, high, critical")
	}
	if r.WindowSeconds < MinSecurityRuleWindowSeconds || r.WindowSeconds > MaxSecurityRuleWindowSeconds {
		return newValidation("window_seconds", "must be between 1 and 86400 (24 hours)")
	}
	if r.ThresholdCount < MinSecurityRuleThresholdCount || r.ThresholdCount > MaxSecurityRuleThresholdCount {
		return newValidation("threshold_count", "must be between 1 and 1000000")
	}
	if !r.IncidentSeverity.Valid() {
		return newValidation("incident_severity", "must be one of: critical, high, medium, low")
	}
	if !r.LastState.Valid() {
		return newValidation("last_state", "must be one of: ok, firing")
	}
	return nil
}

// NewSecurityRule builds a validated, enabled SecurityRule, freshly minted and
// never yet evaluated (LastState=ok, CurrentIncidentID=nil). tenantID is
// always taken from the bound connection's context, never from request data,
// the same rule NewAlertRule follows. MinSeverity is not a constructor
// parameter — it defaults to DefaultSecurityRuleMinSeverity, the same
// after-construction default AlertRule.SymptomClass gets, overridable by the
// caller before Create or later via SecurityRulePatch.
func NewSecurityRule(
	tenantID, assetID, observationType string, windowSeconds, thresholdCount int, incidentSeverity IncidentSeverity,
) (*SecurityRule, error) {
	r := &SecurityRule{
		RuleID:           NewID(),
		TenantID:         strings.TrimSpace(tenantID),
		AssetID:          strings.TrimSpace(assetID),
		ObservationType:  strings.TrimSpace(observationType),
		MinSeverity:      DefaultSecurityRuleMinSeverity,
		WindowSeconds:    windowSeconds,
		ThresholdCount:   thresholdCount,
		IncidentSeverity: incidentSeverity,
		Enabled:          true,
		LastState:        SecurityRuleStateOK,
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// SecurityRulePatch carries the fields SecurityRuleRepository.Update may
// change; a nil pointer leaves that field unchanged. AssetID and
// ObservationType are deliberately absent — like AlertRulePatch's AssetID/
// Metric — what a rule watches is fixed at creation, not a field to repoint
// later: delete and recreate instead. LastState/CurrentIncidentID are
// likewise absent: only the future detector's background counterpart
// (E8.1b-2) sets them — see SecurityRule's doc comment. Nothing in this
// story writes them at all.
type SecurityRulePatch struct {
	MinSeverity      *ObservationSeverity
	WindowSeconds    *int
	ThresholdCount   *int
	IncidentSeverity *IncidentSeverity
	Enabled          *bool
}

// SecurityRuleRepository administers security_rule: E8.1b-1's threshold-over-
// window config over security_observation. CONFIG ONLY — no detector, worker
// or correlation reads this repository or security_observation to evaluate a
// rule; that is E8.1b-2, a later, separate story.
//
// security_rule is TENANT-OWNED: it is in TenantOwnedTables and carries
// row-level security, so this repository takes no tenant argument anywhere —
// the bound connection already is the boundary (ADR-TENANCY-002), exactly
// like AlertRuleRepository.
type SecurityRuleRepository interface {
	// Create inserts a rule. AssetID must already be visible to the caller's
	// tenant — re-verified against this store's own tenant-scoped connection
	// before the row is written (ADR-ASSET-001 §6, extended here, mirroring
	// AlertRuleRepository.Create); an asset_id the tenant cannot see returns
	// ErrNotFound.
	Create(ctx context.Context, r *SecurityRule) (*SecurityRule, error)
	Get(ctx context.Context, ruleID string) (*SecurityRule, error)
	// List returns a page of the caller's rules, keyset-paginated over
	// rule_id.
	List(ctx context.Context, limit int, after string) ([]*SecurityRule, error)
	// Update changes one or more fields under optimistic locking. Returns
	// ErrVersionMismatch on a stale rowVersion, ErrNotFound if ruleID does not
	// name a rule visible to this tenant.
	Update(ctx context.Context, ruleID string, rowVersion int64, patch SecurityRulePatch) (*SecurityRule, error)
	// Delete removes a rule, or returns ErrNotFound. Carries no row_version —
	// see ADR-HARD-003, which applies here unchanged.
	Delete(ctx context.Context, ruleID string) error
}
