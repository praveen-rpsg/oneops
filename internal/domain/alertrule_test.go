package domain

import "testing"

// TestAlertSymptomClass_Valid pins the enum (E3.4): exactly availability,
// resource and unspecified are defined; everything else — including a
// plausible-looking near-miss and the empty string — is not.
func TestAlertSymptomClass_Valid(t *testing.T) {
	tests := []struct {
		class AlertSymptomClass
		want  bool
	}{
		{AlertSymptomClassAvailability, true},
		{AlertSymptomClassResource, true},
		{AlertSymptomClassUnspecified, true},
		{"", false},
		{"Availability", false},
		{"reachability", false},
		{"resources", false},
		{"AVAILABILITY", false},
	}
	for _, tt := range tests {
		if got := tt.class.Valid(); got != tt.want {
			t.Errorf("AlertSymptomClass(%q).Valid() = %v, want %v", tt.class, got, tt.want)
		}
	}
}

// TestDefaultAlertSymptomClass_IsUnspecified pins the CTO-locked default: an
// operator who says nothing gets "unspecified", never an inferred class.
func TestDefaultAlertSymptomClass_IsUnspecified(t *testing.T) {
	if DefaultAlertSymptomClass != AlertSymptomClassUnspecified {
		t.Errorf("DefaultAlertSymptomClass = %q, want %q", DefaultAlertSymptomClass, AlertSymptomClassUnspecified)
	}
}

func validAlertRuleForSymptomClass() *AlertRule {
	return &AlertRule{
		RuleID: "rule-1", TenantID: "tenant-a", AssetID: "asset-1",
		Metric: "cpu_utilization", Comparator: ComparatorGT, Threshold: 90,
		ForDuration: 60, Severity: AlertSeverityWarning,
		SymptomClass: AlertSymptomClassUnspecified,
		LastState:    AlertRuleStateOK, FlapDwellSeconds: DefaultAlertRuleFlapDwellSeconds,
	}
}

// TestAlertRule_NewAlertRuleDefaultsSymptomClassToUnspecified proves the
// constructor mirrors FlapDwellSeconds's own default-on-construct treatment
// (E3.4): a caller who does not set SymptomClass at all — NewAlertRule takes
// no symptom_class parameter, exactly like it takes no flap_dwell_seconds
// parameter — gets "unspecified", never a zero value that would fail
// Validate.
func TestAlertRule_NewAlertRuleDefaultsSymptomClassToUnspecified(t *testing.T) {
	r, err := NewAlertRule("tenant-a", "asset-1", "cpu_utilization", ComparatorGT, 90, 60, AlertSeverityWarning)
	if err != nil {
		t.Fatalf("new alert rule: %v", err)
	}
	if r.SymptomClass != AlertSymptomClassUnspecified {
		t.Errorf("SymptomClass = %q, want %q", r.SymptomClass, AlertSymptomClassUnspecified)
	}
	if err := r.Validate(); err != nil {
		t.Errorf("a freshly constructed rule must validate: %v", err)
	}
}

// TestAlertRule_ValidateRejectsUnknownSymptomClass is the mutation-provable
// bite: an out-of-enum symptom_class fails Validate with a field-level
// reason naming "symptom_class", the same defense-in-depth the DB CHECK
// constraint (ck_alert_rule_symptom_class) applies a second time. Removing
// the `!r.SymptomClass.Valid()` guard from AlertRule.Validate flips this test
// to a false pass (a bad value would validate) — the guard bites.
func TestAlertRule_ValidateRejectsUnknownSymptomClass(t *testing.T) {
	for _, bad := range []AlertSymptomClass{"", "bogus", "Availability", "RESOURCE"} {
		r := validAlertRuleForSymptomClass()
		r.SymptomClass = bad
		err := r.Validate()
		ve, ok := AsValidation(err)
		if !ok {
			t.Fatalf("SymptomClass=%q: Validate() err = %v, want a *ValidationError", bad, err)
		}
		if ve.Field != "symptom_class" {
			t.Errorf("SymptomClass=%q: Validate() field = %q, want %q", bad, ve.Field, "symptom_class")
		}
	}
}

// TestAlertRule_ValidateAcceptsEveryDefinedSymptomClass proves every member
// of the enum — not just the default — validates cleanly, so an operator can
// actually set availability/resource explicitly, not merely leave the
// default in place.
func TestAlertRule_ValidateAcceptsEveryDefinedSymptomClass(t *testing.T) {
	for _, good := range []AlertSymptomClass{
		AlertSymptomClassAvailability, AlertSymptomClassResource, AlertSymptomClassUnspecified,
	} {
		r := validAlertRuleForSymptomClass()
		r.SymptomClass = good
		if err := r.Validate(); err != nil {
			t.Errorf("SymptomClass=%q: Validate() = %v, want nil", good, err)
		}
	}
}

// TestAlertRulePatch_SymptomClassIsAPointer confirms the patch shape mirrors
// Severity's: a nil pointer means "leave unchanged", distinguishing "not
// supplied" from "explicitly set to the zero value" — there is no valid zero
// value for this enum, but the pointer discipline is the same one every other
// patchable field on AlertRulePatch uses.
func TestAlertRulePatch_SymptomClassIsAPointer(t *testing.T) {
	var patch AlertRulePatch
	if patch.SymptomClass != nil {
		t.Fatal("a zero-value AlertRulePatch must leave symptom_class untouched")
	}
	sc := AlertSymptomClassAvailability
	patch.SymptomClass = &sc
	if *patch.SymptomClass != AlertSymptomClassAvailability {
		t.Errorf("patch.SymptomClass = %q, want %q", *patch.SymptomClass, AlertSymptomClassAvailability)
	}
}
