package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func validSecurityResponseRule() *SecurityResponseRule {
	return &SecurityResponseRule{
		RuleID: "srr-1", TenantID: "tenant-a", Name: "notify-on-high",
		MinSeverity:  IncidentSeverityHigh,
		ActionType:   "http",
		ActionConfig: json.RawMessage(`{"url":"https://example.com/hook"}`),
		Enabled:      true,
	}
}

// TestValidSecurityResponseActionType_OnlyHTTPAndNotification is the
// allowlist's own pin: exactly "http" and "notification" are safe. Every
// other action policy.DefaultRegistry can run — "command" (arbitrary
// execution), "email", any invented destructive/response verb (isolate,
// block, disable, quarantine) — is refused.
func TestValidSecurityResponseActionType_OnlyHTTPAndNotification(t *testing.T) {
	tests := []struct {
		actionType string
		want       bool
	}{
		{"http", true},
		{"notification", true},
		{"command", false},
		{"email", false},
		{"isolate", false},
		{"block", false},
		{"disable", false},
		{"quarantine", false},
		{"", false},
		{"HTTP", false}, // case-sensitive: no silent normalization of an unsafe type into a safe one
	}
	for _, tt := range tests {
		if got := ValidSecurityResponseActionType(tt.actionType); got != tt.want {
			t.Errorf("ValidSecurityResponseActionType(%q) = %v, want %v", tt.actionType, got, tt.want)
		}
	}
}

// TestNewSecurityResponseRule_RejectsCommandAction is the story's own
// make-or-break proof: a rule naming the "command" action type — arbitrary
// execution, the exact hazard this story exists to keep out — is refused by
// the constructor with a field-level reason naming action_type, never
// silently accepted or downgraded.
func TestNewSecurityResponseRule_RejectsCommandAction(t *testing.T) {
	_, err := NewSecurityResponseRule("tenant-a", "run-command", IncidentSeverityHigh, nil, "command", nil)
	ve, ok := AsValidation(err)
	if !ok {
		t.Fatalf("NewSecurityResponseRule with action_type=command: err = %v, want a *ValidationError", err)
	}
	if ve.Field != "action_type" {
		t.Errorf("field = %q, want %q", ve.Field, "action_type")
	}
}

// TestNewSecurityResponseRule_RejectsEveryNonSafeActionType sweeps a wider
// set of non-safe action types (including ones that are not registered
// anywhere) through the constructor, proving refusal is a closed allowlist
// check, not a hand-picked blocklist of "command" alone.
func TestNewSecurityResponseRule_RejectsEveryNonSafeActionType(t *testing.T) {
	for _, bad := range []string{"command", "email", "isolate", "block", "disable", "quarantine", "", "bogus"} {
		_, err := NewSecurityResponseRule("tenant-a", "rule", IncidentSeverityHigh, nil, bad, nil)
		ve, ok := AsValidation(err)
		if !ok || ve.Field != "action_type" {
			t.Errorf("action_type=%q: err = %v, want an action_type ValidationError", bad, err)
		}
	}
}

// TestNewSecurityResponseRule_AcceptsEverySafeActionType proves both members
// of the allowlist are actually usable, not merely permitted in principle.
func TestNewSecurityResponseRule_AcceptsEverySafeActionType(t *testing.T) {
	for _, good := range []string{"http", "notification"} {
		r, err := NewSecurityResponseRule("tenant-a", "rule", IncidentSeverityHigh, nil, good, nil)
		if err != nil {
			t.Errorf("action_type=%q: NewSecurityResponseRule() = %v, want nil error", good, err)
			continue
		}
		if r.ActionType != good {
			t.Errorf("ActionType = %q, want %q", r.ActionType, good)
		}
	}
}

// TestNewSecurityResponseRule_DefaultsAndValidates proves the constructor
// mints an id, starts enabled, defaults an empty ActionConfig to "{}" (valid
// JSON a jsonb column accepts), and trims Name/AssetID.
func TestNewSecurityResponseRule_DefaultsAndValidates(t *testing.T) {
	assetID := "  asset-1  "
	r, err := NewSecurityResponseRule("tenant-a", "  notify  ", IncidentSeverityHigh, &assetID, "notification", nil)
	if err != nil {
		t.Fatalf("new security response rule: %v", err)
	}
	if r.RuleID == "" {
		t.Error("rule_id must be server-minted, not empty")
	}
	if !r.Enabled {
		t.Error("a freshly constructed rule must be enabled")
	}
	if r.Name != "notify" {
		t.Errorf("Name = %q, want trimmed %q", r.Name, "notify")
	}
	if r.AssetID == nil || *r.AssetID != "asset-1" {
		t.Errorf("AssetID = %v, want trimmed \"asset-1\"", r.AssetID)
	}
	if string(r.ActionConfig) != "{}" {
		t.Errorf("ActionConfig = %q, want default %q", r.ActionConfig, "{}")
	}
	if err := r.Validate(); err != nil {
		t.Errorf("a freshly constructed rule must validate: %v", err)
	}
}

// TestNewSecurityResponseRule_NilAssetIDMeansEveryAsset proves the optional
// AssetID is genuinely optional: nil is a valid, unscoped rule.
func TestNewSecurityResponseRule_NilAssetIDMeansEveryAsset(t *testing.T) {
	r, err := NewSecurityResponseRule("tenant-a", "notify", IncidentSeverityHigh, nil, "http", json.RawMessage(`{"url":"https://example.com"}`))
	if err != nil {
		t.Fatalf("new security response rule: %v", err)
	}
	if r.AssetID != nil {
		t.Errorf("AssetID = %v, want nil", r.AssetID)
	}
}

// TestSecurityResponseRule_ValidateRejectsBlankIdentifiers mirrors
// SecurityRule's identical isolation-key discipline.
func TestSecurityResponseRule_ValidateRejectsBlankIdentifiers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SecurityResponseRule)
		field  string
	}{
		{"rule_id", func(r *SecurityResponseRule) { r.RuleID = "" }, "rule_id"},
		{"tenant_id", func(r *SecurityResponseRule) { r.TenantID = "  " }, "tenant_id"},
		{"name empty", func(r *SecurityResponseRule) { r.Name = "" }, "name"},
		{"name blank", func(r *SecurityResponseRule) { r.Name = "   " }, "name"},
	}
	for _, tt := range tests {
		r := validSecurityResponseRule()
		tt.mutate(r)
		err := r.Validate()
		ve, ok := AsValidation(err)
		if !ok {
			t.Fatalf("%s: Validate() err = %v, want a *ValidationError", tt.name, err)
		}
		if ve.Field != tt.field {
			t.Errorf("%s: Validate() field = %q, want %q", tt.name, ve.Field, tt.field)
		}
	}
}

// TestSecurityResponseRule_ValidateNameLength pins the 200-character bound.
func TestSecurityResponseRule_ValidateNameLength(t *testing.T) {
	r := validSecurityResponseRule()
	r.Name = strings.Repeat("x", MaxSecurityResponseRuleNameLength+1)
	ve, ok := AsValidation(r.Validate())
	if !ok || ve.Field != "name" {
		t.Errorf("Validate() = %v, want a name ValidationError", r.Validate())
	}
}

// TestSecurityResponseRule_ValidateRejectsBlankAssetIDWhenSupplied proves an
// explicitly blank (but non-nil) AssetID is refused — omit the field
// entirely to scope to every asset, matching Incident.Validate's own
// AssetID discipline.
func TestSecurityResponseRule_ValidateRejectsBlankAssetIDWhenSupplied(t *testing.T) {
	r := validSecurityResponseRule()
	blank := "   "
	r.AssetID = &blank
	ve, ok := AsValidation(r.Validate())
	if !ok || ve.Field != "asset_id" {
		t.Errorf("Validate() = %v, want an asset_id ValidationError", r.Validate())
	}
}

// TestSecurityResponseRule_ValidateRejectsUnknownMinSeverity mirrors
// SecurityRule's identical enum guard.
func TestSecurityResponseRule_ValidateRejectsUnknownMinSeverity(t *testing.T) {
	for _, bad := range []IncidentSeverity{"", "bogus", "Info", "urgent"} {
		r := validSecurityResponseRule()
		r.MinSeverity = bad
		ve, ok := AsValidation(r.Validate())
		if !ok || ve.Field != "min_severity" {
			t.Errorf("MinSeverity=%q: Validate() = %v, want a min_severity ValidationError", bad, r.Validate())
		}
	}
}

// TestSecurityResponseRule_ValidateActionConfigMustBeValidJSON proves a
// non-empty ActionConfig that is not valid JSON is refused rather than
// stored opaquely and failing only when the action later tries to parse it.
func TestSecurityResponseRule_ValidateActionConfigMustBeValidJSON(t *testing.T) {
	r := validSecurityResponseRule()
	r.ActionConfig = json.RawMessage(`{not json`)
	ve, ok := AsValidation(r.Validate())
	if !ok || ve.Field != "action_config" {
		t.Errorf("Validate() = %v, want an action_config ValidationError", r.Validate())
	}
}

// TestSecurityResponseRule_ValidateActionConfigLengthBound pins the
// 8192-byte ceiling.
func TestSecurityResponseRule_ValidateActionConfigLengthBound(t *testing.T) {
	r := validSecurityResponseRule()
	// A syntactically valid but oversized JSON string value.
	huge := `{"url":"https://example.com/` + strings.Repeat("a", MaxSecurityResponseActionConfigLength) + `"}`
	r.ActionConfig = json.RawMessage(huge)
	ve, ok := AsValidation(r.Validate())
	if !ok || ve.Field != "action_config" {
		t.Errorf("Validate() = %v, want an action_config ValidationError", r.Validate())
	}
}

// TestSecurityResponseRulePatch_FieldsArePointers confirms the patch shape:
// a zero-value patch leaves everything unchanged. AssetID/ActionType are
// deliberately absent from the type itself (see SecurityResponseRulePatch's
// own doc comment) — there is no field to assert nil for them.
func TestSecurityResponseRulePatch_FieldsArePointers(t *testing.T) {
	var patch SecurityResponseRulePatch
	if patch.Name != nil || patch.MinSeverity != nil || patch.ActionConfig != nil || patch.Enabled != nil {
		t.Fatal("a zero-value SecurityResponseRulePatch must leave every field untouched")
	}
	sev := IncidentSeverityCritical
	patch.MinSeverity = &sev
	if *patch.MinSeverity != IncidentSeverityCritical {
		t.Errorf("patch.MinSeverity = %q, want %q", *patch.MinSeverity, IncidentSeverityCritical)
	}
}

// TestIncidentSeverity_AtLeast pins the ordering critical > high > medium >
// low that SecurityResponseRule's MinSeverity threshold match depends on.
func TestIncidentSeverity_AtLeast(t *testing.T) {
	tests := []struct {
		severity, min IncidentSeverity
		want          bool
	}{
		{IncidentSeverityCritical, IncidentSeverityHigh, true},
		{IncidentSeverityHigh, IncidentSeverityHigh, true},
		{IncidentSeverityMedium, IncidentSeverityHigh, false},
		{IncidentSeverityLow, IncidentSeverityCritical, false},
		{IncidentSeverityCritical, IncidentSeverityLow, true},
		{IncidentSeverityLow, IncidentSeverityLow, true},
	}
	for _, tt := range tests {
		if got := tt.severity.AtLeast(tt.min); got != tt.want {
			t.Errorf("%s.AtLeast(%s) = %v, want %v", tt.severity, tt.min, got, tt.want)
		}
	}
}
