package domain

import "testing"

func validSecurityRule() *SecurityRule {
	return &SecurityRule{
		RuleID: "rule-1", TenantID: "tenant-a", AssetID: "asset-1",
		ObservationType: "port_scan", MinSeverity: ObservationSeverityInfo,
		WindowSeconds: 300, ThresholdCount: 5,
		IncidentSeverity: IncidentSeverityHigh,
		Enabled:          true,
		LastState:        SecurityRuleStateOK,
	}
}

// TestSecurityRuleState_Valid pins the enum: exactly ok and firing are
// defined; everything else — including the empty string — is not.
func TestSecurityRuleState_Valid(t *testing.T) {
	tests := []struct {
		state SecurityRuleState
		want  bool
	}{
		{SecurityRuleStateOK, true},
		{SecurityRuleStateFiring, true},
		{"", false},
		{"OK", false},
		{"Firing", false},
		{"bogus", false},
	}
	for _, tt := range tests {
		if got := tt.state.Valid(); got != tt.want {
			t.Errorf("SecurityRuleState(%q).Valid() = %v, want %v", tt.state, got, tt.want)
		}
	}
}

// TestDefaultSecurityRuleMinSeverity_IsInfo pins the least-restrictive
// default: an operator who says nothing counts every observation of the
// rule's ObservationType, never a silently narrowed subset.
func TestDefaultSecurityRuleMinSeverity_IsInfo(t *testing.T) {
	if DefaultSecurityRuleMinSeverity != ObservationSeverityInfo {
		t.Errorf("DefaultSecurityRuleMinSeverity = %q, want %q", DefaultSecurityRuleMinSeverity, ObservationSeverityInfo)
	}
}

// TestNewSecurityRule_DefaultsAndValidates proves the constructor mirrors
// NewAlertRule's shape: a fresh rule starts enabled, at last_state=ok, with
// no linked incident, and MinSeverity defaulted even though the constructor
// takes no such parameter.
func TestNewSecurityRule_DefaultsAndValidates(t *testing.T) {
	r, err := NewSecurityRule("tenant-a", "asset-1", "port_scan", 300, 5, IncidentSeverityHigh)
	if err != nil {
		t.Fatalf("new security rule: %v", err)
	}
	if r.RuleID == "" {
		t.Error("rule_id must be server-minted, not empty")
	}
	if !r.Enabled {
		t.Error("a freshly constructed rule must be enabled")
	}
	if r.LastState != SecurityRuleStateOK {
		t.Errorf("LastState = %q, want %q", r.LastState, SecurityRuleStateOK)
	}
	if r.CurrentIncidentID != nil {
		t.Errorf("CurrentIncidentID = %v, want nil", r.CurrentIncidentID)
	}
	if r.MinSeverity != DefaultSecurityRuleMinSeverity {
		t.Errorf("MinSeverity = %q, want default %q", r.MinSeverity, DefaultSecurityRuleMinSeverity)
	}
	if err := r.Validate(); err != nil {
		t.Errorf("a freshly constructed rule must validate: %v", err)
	}
}

// TestSecurityRule_ValidateRejectsBlankIdentifiers proves the same
// isolation-key discipline AlertRule.Validate enforces: rule_id/tenant_id/
// asset_id must not be empty.
func TestSecurityRule_ValidateRejectsBlankIdentifiers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*SecurityRule)
		field  string
	}{
		{"rule_id", func(r *SecurityRule) { r.RuleID = "" }, "rule_id"},
		{"tenant_id", func(r *SecurityRule) { r.TenantID = "  " }, "tenant_id"},
		{"asset_id", func(r *SecurityRule) { r.AssetID = "" }, "asset_id"},
	}
	for _, tt := range tests {
		r := validSecurityRule()
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

// TestSecurityRule_ValidateObservationType proves the length/format bounds
// mirror SecurityObservation.ObservationType's own (E8.1a).
func TestSecurityRule_ValidateObservationType(t *testing.T) {
	tooLong := make([]byte, MaxObservationTypeLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{"empty", "", false},
		{"blank", "   ", false},
		{"too long", string(tooLong), false},
		{"uppercase", "Port_Scan", false},
		{"hyphenated", "port-scan", false},
		{"valid", "port_scan", true},
		{"valid with digits", "failed_login_v2", true},
	}
	for _, tt := range tests {
		r := validSecurityRule()
		r.ObservationType = tt.value
		err := r.Validate()
		if tt.valid && err != nil {
			t.Errorf("%s: Validate() = %v, want nil", tt.name, err)
			continue
		}
		if !tt.valid {
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("%s: Validate() err = %v, want a *ValidationError", tt.name, err)
			}
			if ve.Field != "observation_type" {
				t.Errorf("%s: Validate() field = %q, want %q", tt.name, ve.Field, "observation_type")
			}
		}
	}
}

// TestSecurityRule_ValidateRejectsUnknownMinSeverity is the mutation-provable
// bite: an out-of-enum min_severity fails Validate with a field-level reason
// naming "min_severity". Removing the !r.MinSeverity.Valid() guard flips this
// test to a false pass.
func TestSecurityRule_ValidateRejectsUnknownMinSeverity(t *testing.T) {
	for _, bad := range []ObservationSeverity{"", "bogus", "Info", "URGENT"} {
		r := validSecurityRule()
		r.MinSeverity = bad
		err := r.Validate()
		ve, ok := AsValidation(err)
		if !ok {
			t.Fatalf("MinSeverity=%q: Validate() err = %v, want a *ValidationError", bad, err)
		}
		if ve.Field != "min_severity" {
			t.Errorf("MinSeverity=%q: Validate() field = %q, want %q", bad, ve.Field, "min_severity")
		}
	}
}

// TestSecurityRule_ValidateAcceptsEveryDefinedMinSeverity proves every member
// of ObservationSeverity validates cleanly as min_severity.
func TestSecurityRule_ValidateAcceptsEveryDefinedMinSeverity(t *testing.T) {
	for _, good := range []ObservationSeverity{
		ObservationSeverityInfo, ObservationSeverityLow, ObservationSeverityMedium,
		ObservationSeverityHigh, ObservationSeverityCritical,
	} {
		r := validSecurityRule()
		r.MinSeverity = good
		if err := r.Validate(); err != nil {
			t.Errorf("MinSeverity=%q: Validate() = %v, want nil", good, err)
		}
	}
}

// TestSecurityRule_ValidateWindowSecondsBounds pins the 1..86400 bound.
func TestSecurityRule_ValidateWindowSecondsBounds(t *testing.T) {
	tests := []struct {
		window int
		valid  bool
	}{
		{0, false},
		{-1, false},
		{1, true},
		{86400, true},
		{86401, false},
	}
	for _, tt := range tests {
		r := validSecurityRule()
		r.WindowSeconds = tt.window
		err := r.Validate()
		if tt.valid && err != nil {
			t.Errorf("window_seconds=%d: Validate() = %v, want nil", tt.window, err)
			continue
		}
		if !tt.valid {
			ve, ok := AsValidation(err)
			if !ok || ve.Field != "window_seconds" {
				t.Errorf("window_seconds=%d: Validate() = %v, want a window_seconds ValidationError", tt.window, err)
			}
		}
	}
}

// TestSecurityRule_ValidateThresholdCountBounds pins the
// 1..MaxSecurityRuleThresholdCount bound.
func TestSecurityRule_ValidateThresholdCountBounds(t *testing.T) {
	tests := []struct {
		count int
		valid bool
	}{
		{0, false},
		{-1, false},
		{1, true},
		{MaxSecurityRuleThresholdCount, true},
		{MaxSecurityRuleThresholdCount + 1, false},
	}
	for _, tt := range tests {
		r := validSecurityRule()
		r.ThresholdCount = tt.count
		err := r.Validate()
		if tt.valid && err != nil {
			t.Errorf("threshold_count=%d: Validate() = %v, want nil", tt.count, err)
			continue
		}
		if !tt.valid {
			ve, ok := AsValidation(err)
			if !ok || ve.Field != "threshold_count" {
				t.Errorf("threshold_count=%d: Validate() = %v, want a threshold_count ValidationError", tt.count, err)
			}
		}
	}
}

// TestSecurityRule_ValidateRejectsUnknownIncidentSeverity proves
// incident_severity is validated against the INCIDENT severity vocabulary
// (critical/high/medium/low), not ObservationSeverity's five-level scale —
// "critical" is shared, but "info" (a valid ObservationSeverity) must be
// rejected here.
func TestSecurityRule_ValidateRejectsUnknownIncidentSeverity(t *testing.T) {
	for _, bad := range []IncidentSeverity{"", "bogus", "info", "warning"} {
		r := validSecurityRule()
		r.IncidentSeverity = bad
		err := r.Validate()
		ve, ok := AsValidation(err)
		if !ok {
			t.Fatalf("IncidentSeverity=%q: Validate() err = %v, want a *ValidationError", bad, err)
		}
		if ve.Field != "incident_severity" {
			t.Errorf("IncidentSeverity=%q: Validate() field = %q, want %q", bad, ve.Field, "incident_severity")
		}
	}
}

// TestSecurityRule_ValidateAcceptsEveryDefinedIncidentSeverity proves every
// member of IncidentSeverity validates cleanly.
func TestSecurityRule_ValidateAcceptsEveryDefinedIncidentSeverity(t *testing.T) {
	for _, good := range []IncidentSeverity{
		IncidentSeverityCritical, IncidentSeverityHigh, IncidentSeverityMedium, IncidentSeverityLow,
	} {
		r := validSecurityRule()
		r.IncidentSeverity = good
		if err := r.Validate(); err != nil {
			t.Errorf("IncidentSeverity=%q: Validate() = %v, want nil", good, err)
		}
	}
}

// TestSecurityRule_ValidateRejectsUnknownLastState proves last_state is
// checked even though nothing in this story sets it to anything other than
// SecurityRuleStateOK — defense in depth, the same reason AlertRule.Validate
// checks LastState.
func TestSecurityRule_ValidateRejectsUnknownLastState(t *testing.T) {
	r := validSecurityRule()
	r.LastState = "bogus"
	err := r.Validate()
	ve, ok := AsValidation(err)
	if !ok || ve.Field != "last_state" {
		t.Errorf("Validate() = %v, want a last_state ValidationError", err)
	}
}

// TestSecurityRulePatch_FieldsArePointers confirms the patch shape mirrors
// AlertRulePatch's: a zero-value patch leaves everything unchanged,
// distinguishing "not supplied" from "explicitly set to the zero value".
func TestSecurityRulePatch_FieldsArePointers(t *testing.T) {
	var patch SecurityRulePatch
	if patch.MinSeverity != nil || patch.WindowSeconds != nil || patch.ThresholdCount != nil ||
		patch.IncidentSeverity != nil || patch.Enabled != nil {
		t.Fatal("a zero-value SecurityRulePatch must leave every field untouched")
	}
	sev := ObservationSeverityHigh
	patch.MinSeverity = &sev
	if *patch.MinSeverity != ObservationSeverityHigh {
		t.Errorf("patch.MinSeverity = %q, want %q", *patch.MinSeverity, ObservationSeverityHigh)
	}
}
