package domain

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// SafeSecurityResponseActionTypes is the SAFE, outbound-only action-type
// allowlist a SecurityResponseRule's ActionType is validated against — the
// strategic bound this story (E8.5, ADR-SOC-010) exists to enforce. It names
// the exact action-type STRINGS policy.DefaultRegistry registers under
// "http" (webhook) and "notification" (internal) — reusing that registry for
// EXECUTION rather than inventing a parallel action vocabulary — but it is
// its OWN closed allowlist, not a reflection of the registry's full
// contents: policy.DefaultRegistry also registers "email" and "command"
// (arbitrary execution), and any future destructive/response action
// (isolate, block, disable, quarantine) would likewise be registered there
// long before it is safe here. A SecurityResponseRule can therefore only
// ever ask a human or a downstream system to look at something — it can
// never act on the incident's own asset. Destructive/autonomous response is
// DEFERRED behind the machine-action attestation model
// (docs/PLATFORM-BUILD-PLAN.md E8.5); this allowlist is the enforcement
// point for that deferral, checked at Create/Update time, not merely
// documented.
var SafeSecurityResponseActionTypes = []string{"http", "notification"}

// ValidSecurityResponseActionType reports whether actionType is in the SAFE
// allowlist above.
func ValidSecurityResponseActionType(actionType string) bool {
	for _, t := range SafeSecurityResponseActionTypes {
		if actionType == t {
			return true
		}
	}
	return false
}

// MaxSecurityResponseRuleNameLength bounds a free-text field that reaches
// consoles and logs, the same bound EscalationPolicy.Name/Team.Name carry.
const MaxSecurityResponseRuleNameLength = 200

// MaxSecurityResponseActionConfigLength bounds the raw JSON stored in
// ActionConfig — opaque to this package, validated by the action
// implementation itself when it runs (the same split policy.ActionSpec.
// Config's own doc comment draws) — but still bounded: an unbounded jsonb
// value in a config row is the same storage/rendering hazard
// MaxIncidentDescriptionLength's own doc comment states for free text.
const MaxSecurityResponseActionConfigLength = 8192

// SecurityResponseRule is a security-response-automation config (E8.5,
// ADR-SOC-010): when a NEW security-sourced Incident's severity is at least
// MinSeverity (and, if AssetID is set, the incident is linked to that one
// CI), run ActionType with ActionConfig via the EXISTING policy.Registry —
// exactly once per (incident, rule), recorded by the append-only
// security_response_dispatch ledger (see SecurityResponder's own doc
// comment for the exactly-once discipline).
//
// This is deliberately a "condition + one safe ActionSpec" ROW, NOT a
// reified Workflow/Playbook (Vol II §5.3 reduced those away): there is no
// ordered step sequence, no branching, and no state machine here — one rule
// runs one action. Compare policy.Policy.Steps (ADR-POLICY-001), which
// composes several actions for the GOVERNANCE audit-event pipeline; nothing
// about that composed shape is reused or needed here.
//
// This engine ONLY ever reads security-sourced incidents
// (Source == IncidentSourceSecurity) — the source is fixed by which engine
// evaluates a rule, not a field an operator sets, the same non-decision
// SecurityRule/IOC make for their own detection paths.
type SecurityResponseRule struct {
	RuleID   string
	TenantID string
	Name     string
	// MinSeverity is the threshold: this rule fires on an incident whose
	// Severity.AtLeast(MinSeverity) is true.
	MinSeverity IncidentSeverity
	// AssetID, when set, scopes this rule to incidents linked to one
	// Configuration Item — re-verified against the caller's own tenant-scoped
	// connection before being written, the same defense every other
	// asset-scoped config row in this codebase applies (ADR-ASSET-001 §6).
	// Nil means "every security incident this tenant raises, regardless of
	// asset". Fixed at creation — not patchable, the same treatment
	// SecurityRule.ObservationType/IOC.IndicatorType get: delete and recreate
	// to re-scope.
	AssetID *string
	// ActionType names the SAFE action the policy Registry runs — validated
	// against SafeSecurityResponseActionTypes, never any other action type
	// the Registry happens to have registered (see that allowlist's own doc
	// comment). Fixed at creation for the identical reason AssetID is: a
	// rule that could be silently repointed from "notification" to a
	// destructive action via PATCH would defeat the create-time allowlist
	// check entirely.
	ActionType string
	// ActionConfig is opaque configuration for ActionType — validated by the
	// action implementation itself when it runs, not by this package (the
	// same split policy.ActionSpec.Config's own doc comment draws).
	ActionConfig json.RawMessage
	Enabled      bool
	RowVersion   int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Validate enforces the invariants the database also enforces, so a bad rule
// fails with a field-level reason before it reaches the store.
func (r *SecurityResponseRule) Validate() error {
	if strings.TrimSpace(r.RuleID) == "" {
		return newValidation("rule_id", "must not be empty")
	}
	if strings.TrimSpace(r.TenantID) == "" {
		return newValidation("tenant_id", "must not be empty; it is the row's isolation key")
	}
	name := strings.TrimSpace(r.Name)
	if name == "" {
		return newValidation("name", "must not be empty")
	}
	if len(name) > MaxSecurityResponseRuleNameLength {
		return newValidation("name", "must be at most 200 characters")
	}
	if !r.MinSeverity.Valid() {
		return newValidation("min_severity", "must be one of: critical, high, medium, low")
	}
	if r.AssetID != nil && strings.TrimSpace(*r.AssetID) == "" {
		return newValidation("asset_id", "must not be blank when supplied; omit it to scope to every asset")
	}
	if !ValidSecurityResponseActionType(r.ActionType) {
		return newValidation("action_type",
			"must be one of the SAFE actions: http, notification — destructive or arbitrary-execution actions are refused")
	}
	if len(r.ActionConfig) > MaxSecurityResponseActionConfigLength {
		return newValidation("action_config", "must be at most 8192 bytes")
	}
	if len(r.ActionConfig) > 0 && !json.Valid(r.ActionConfig) {
		return newValidation("action_config", "must be valid JSON")
	}
	return nil
}

// NewSecurityResponseRule builds a validated, enabled SecurityResponseRule.
// tenantID is always taken from the bound connection's context, never from
// request data, the same rule NewSecurityRule/NewIOC follow. actionConfig
// defaults to "{}" when empty, so a caller omitting it (e.g. the
// "notification" action, which needs no configuration) still stores valid
// JSON rather than an empty byte slice a jsonb column would reject.
func NewSecurityResponseRule(
	tenantID, name string, minSeverity IncidentSeverity, assetID *string, actionType string, actionConfig json.RawMessage,
) (*SecurityResponseRule, error) {
	var trimmedAsset *string
	if assetID != nil {
		trimmed := strings.TrimSpace(*assetID)
		trimmedAsset = &trimmed
	}
	if len(actionConfig) == 0 {
		actionConfig = json.RawMessage("{}")
	}
	r := &SecurityResponseRule{
		RuleID:       NewID(),
		TenantID:     strings.TrimSpace(tenantID),
		Name:         strings.TrimSpace(name),
		MinSeverity:  minSeverity,
		AssetID:      trimmedAsset,
		ActionType:   actionType,
		ActionConfig: actionConfig,
		Enabled:      true,
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// SecurityResponseRulePatch carries the fields SecurityResponseRuleRepository.
// Update may change; a nil pointer leaves that field unchanged. AssetID and
// ActionType are deliberately absent — see SecurityResponseRule's own doc
// comment on each field for why: delete and recreate the rule instead.
type SecurityResponseRulePatch struct {
	Name         *string
	MinSeverity  *IncidentSeverity
	ActionConfig *json.RawMessage
	Enabled      *bool
}

// SecurityResponseRuleRepository administers security_response_rule: E8.5's
// condition + one safe ActionSpec config. CONFIG ONLY — no responder worker
// reads this repository to evaluate a rule; that is
// internal/security.SecurityResponder, a leader-gated background pass built
// over its OWN minimal privileged surface (mirroring
// SecurityRuleDetectorStore/IOCMatcherStore's split from their own admin
// CRUD stores).
//
// security_response_rule is TENANT-OWNED: it is in TenantOwnedTables and
// carries row-level security, so this repository takes no tenant argument
// anywhere — the bound connection already is the boundary
// (ADR-TENANCY-002), exactly like SecurityRuleRepository/IOCRepository.
type SecurityResponseRuleRepository interface {
	// Create inserts a rule, re-validating ActionType against the SAFE
	// allowlist (Validate already does this; the store re-runs it rather
	// than trusting a caller that bypassed the constructor) and, when
	// AssetID is set, re-verifying it against this store's own tenant-scoped
	// connection first — see domain.SecurityRuleRepository.Create's doc
	// comment for the identical asset re-verification discipline. An
	// asset_id the tenant cannot see returns ErrNotFound.
	Create(ctx context.Context, r *SecurityResponseRule) (*SecurityResponseRule, error)
	Get(ctx context.Context, ruleID string) (*SecurityResponseRule, error)
	// List returns a page of the caller's rules, keyset-paginated over
	// rule_id.
	List(ctx context.Context, limit int, after string) ([]*SecurityResponseRule, error)
	// Update changes one or more fields under optimistic locking. Returns
	// ErrVersionMismatch on a stale rowVersion, ErrNotFound if ruleID does not
	// name a rule visible to this tenant.
	Update(ctx context.Context, ruleID string, rowVersion int64, patch SecurityResponseRulePatch) (*SecurityResponseRule, error)
	// Delete removes a rule, or returns ErrNotFound. Carries no row_version —
	// see ADR-HARD-003, which applies here unchanged.
	Delete(ctx context.Context, ruleID string) error
}
