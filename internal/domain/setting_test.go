package domain

import (
	"strings"
	"testing"
)

func TestSettingDefinition_ValidateAcceptsLegalValues(t *testing.T) {
	for _, c := range []struct {
		key, value string
	}{
		{"default_page_size", "50"},
		{"default_page_size", "1"},
		{"default_page_size", "500"},
		{"notification_default_channel", "email"},
		{"notification_default_channel", "webhook"},
		{"notification_default_channel", "inapp"},
		{"notification_max_attempts", "5"},
		{"webhook_delivery_retention_days", "30"},
		{"webhooks_enabled_by_default", "true"},
		{"webhooks_enabled_by_default", "false"},
		{"notification_from_name", "OneOps"},
		{"governance_required_approvals", "1"},
		{"governance_required_approvals", "10"},
	} {
		t.Run(c.key+"="+c.value, func(t *testing.T) {
			def, ok := GetDefinition(c.key)
			if !ok {
				t.Fatalf("definition %q not found", c.key)
			}
			if err := def.Validate(c.value); err != nil {
				t.Errorf("Validate(%q) = %v, want nil", c.value, err)
			}
		})
	}
}

func TestSettingDefinition_ValidateRejectsIllegalValues(t *testing.T) {
	for _, c := range []struct{ key, value string }{
		{"default_page_size", "0"},                         // below Min
		{"default_page_size", "501"},                       // above Max
		{"default_page_size", "fifty"},                     // not an integer
		{"notification_default_channel", "carrier_pigeon"}, // not in Enum
		{"notification_default_channel", ""},
		{"notification_max_attempts", "21"},
		{"notification_max_attempts", "0"},
		{"webhook_delivery_retention_days", "366"},
		{"webhooks_enabled_by_default", "yes"},
		{"webhooks_enabled_by_default", "1"},
		{"notification_from_name", ""},
		{"notification_from_name", strings.Repeat("x", MaxNotificationSubjectLength+1)},
		{"governance_required_approvals", "0"},
		{"governance_required_approvals", "11"},
		{"governance_required_approvals", "one"},
	} {
		t.Run(c.key+"="+c.value, func(t *testing.T) {
			def, ok := GetDefinition(c.key)
			if !ok {
				t.Fatalf("definition %q not found", c.key)
			}
			if err := def.Validate(c.value); err == nil {
				t.Errorf("Validate(%q) accepted an illegal value", c.value)
			}
		})
	}
}

func TestGetDefinition_UnknownKeyIsAbsent(t *testing.T) {
	if _, ok := GetDefinition("not_a_real_setting"); ok {
		t.Error("an undefined key was found in the registry")
	}
}

func TestSettingDefinition_EncodeValueChecksShape(t *testing.T) {
	intDef, _ := GetDefinition("default_page_size")
	if _, err := intDef.EncodeValue("50"); err == nil {
		t.Error("a JSON string was accepted for an int setting")
	}
	if _, err := intDef.EncodeValue(50.5); err == nil {
		t.Error("a fractional number was accepted for an int setting")
	}
	v, err := intDef.EncodeValue(float64(50))
	if err != nil || v != "50" {
		t.Errorf("EncodeValue(50.0) = (%q, %v), want (\"50\", nil)", v, err)
	}

	boolDef, _ := GetDefinition("webhooks_enabled_by_default")
	if _, err := boolDef.EncodeValue("true"); err == nil {
		t.Error("a JSON string was accepted for a bool setting")
	}
	if v, err := boolDef.EncodeValue(true); err != nil || v != "true" {
		t.Errorf("EncodeValue(true) = (%q, %v), want (\"true\", nil)", v, err)
	}

	enumDef, _ := GetDefinition("notification_default_channel")
	if _, err := enumDef.EncodeValue(true); err == nil {
		t.Error("a JSON bool was accepted for an enum setting")
	}
}

func TestSettingDefinition_DecodeValueRendersTypedJSON(t *testing.T) {
	intDef, _ := GetDefinition("default_page_size")
	if v := intDef.DecodeValue("50"); v != int64(50) {
		t.Errorf("DecodeValue(%q) = %v (%T), want int64(50)", "50", v, v)
	}
	boolDef, _ := GetDefinition("webhooks_enabled_by_default")
	if v := boolDef.DecodeValue("true"); v != true {
		t.Errorf("DecodeValue(true) = %v, want true", v)
	}
	if v := boolDef.DecodeValue("false"); v != false {
		t.Errorf("DecodeValue(false) = %v, want false", v)
	}
	strDef, _ := GetDefinition("notification_from_name")
	if v := strDef.DecodeValue("Acme"); v != "Acme" {
		t.Errorf("DecodeValue(%q) = %v, want %q", "Acme", v, "Acme")
	}
}

func TestNewSetting_RejectsUnknownKey(t *testing.T) {
	if _, err := NewSetting("tn-1", "not_a_real_setting", "anything", 0); err == nil {
		t.Error("an unknown key was accepted")
	}
}

func TestNewSetting_RejectsInvalidValue(t *testing.T) {
	if _, err := NewSetting("tn-1", "default_page_size", "not-a-number", 0); err == nil {
		t.Error("an invalid value was accepted")
	}
}

func TestNewSetting_RequiresTenantID(t *testing.T) {
	if _, err := NewSetting("", "default_page_size", "50", 0); err == nil {
		t.Error("a setting with no tenant_id was constructed")
	}
	if _, err := NewSetting("   ", "default_page_size", "50", 0); err == nil {
		t.Error("a setting with a blank tenant_id was constructed")
	}
}

func TestNewSetting_BuildsAValidOverride(t *testing.T) {
	s, err := NewSetting(" tn-1 ", " default_page_size ", "100", 3)
	if err != nil {
		t.Fatalf("NewSetting: %v", err)
	}
	if s.TenantID != "tn-1" || s.Key != "default_page_size" || s.Value != "100" || s.RowVersion != 3 {
		t.Errorf("unexpected setting: %+v", s)
	}
}

// The registry is the allowlist that keeps settings from becoming a junk
// drawer: every definition must at least be internally consistent, or the
// bug is in the registry, not in a caller.
func TestSettingDefinitions_AreInternallyConsistent(t *testing.T) {
	for key, def := range SettingDefinitions {
		if key != def.Key {
			t.Errorf("registry key %q does not match definition.Key %q", key, def.Key)
		}
		if def.Default == "" {
			t.Errorf("%s: has no default", key)
		}
		if err := def.Validate(def.Default); err != nil {
			t.Errorf("%s: its own default fails its own validation: %v", key, err)
		}
		switch def.Type {
		case SettingTypeInt:
			if def.Min >= def.Max {
				t.Errorf("%s: Min (%d) must be less than Max (%d)", key, def.Min, def.Max)
			}
		case SettingTypeEnum:
			if len(def.Enum) == 0 {
				t.Errorf("%s: an enum setting with no allowed values", key)
			}
		case SettingTypeBool, SettingTypeString:
			// no further shape requirement
		default:
			t.Errorf("%s: has an unrecognised type %q", key, def.Type)
		}
	}
	if len(SettingDefinitions) < 4 {
		t.Fatalf("only %d settings are defined; the registry sweep would barely exercise anything", len(SettingDefinitions))
	}
}

// ---------------------------------------------------------- effective resolution

func TestResolveSettings_NoOverridesReturnsAllDefaults(t *testing.T) {
	effective := ResolveSettings(nil)
	if len(effective) != len(SettingDefinitions) {
		t.Fatalf("resolved %d settings, want %d (one per definition)", len(effective), len(SettingDefinitions))
	}
	for _, es := range effective {
		def := SettingDefinitions[es.Key]
		if es.Overridden {
			t.Errorf("%s: reported overridden with no stored override", es.Key)
		}
		if es.Value != def.Default {
			t.Errorf("%s: value = %q, want default %q", es.Key, es.Value, def.Default)
		}
		if es.RowVersion != 0 {
			t.Errorf("%s: row_version = %d, want 0 for a non-overridden setting", es.Key, es.RowVersion)
		}
	}
}

func TestResolveSettings_OverlaysStoredOverrides(t *testing.T) {
	overrides := []*Setting{
		{TenantID: "tn-1", Key: "default_page_size", Value: "200", RowVersion: 4},
	}
	effective := ResolveSettings(overrides)

	var found bool
	for _, es := range effective {
		if es.Key != "default_page_size" {
			// Every other key must still resolve to its default.
			def := SettingDefinitions[es.Key]
			if es.Overridden || es.Value != def.Default {
				t.Errorf("%s: unaffected key was perturbed by an unrelated override: %+v", es.Key, es)
			}
			continue
		}
		found = true
		if !es.Overridden {
			t.Error("default_page_size: expected Overridden = true")
		}
		if es.Value != "200" {
			t.Errorf("default_page_size: value = %q, want %q", es.Value, "200")
		}
		if es.RowVersion != 4 {
			t.Errorf("default_page_size: row_version = %d, want 4", es.RowVersion)
		}
	}
	if !found {
		t.Fatal("default_page_size was not present in the resolved output")
	}
}

// Output order must be deterministic — a caller (and a diff) should never see
// the same overrides render in a different order between two calls.
func TestResolveSettings_OutputIsSortedByKey(t *testing.T) {
	effective := ResolveSettings(nil)
	for i := 1; i < len(effective); i++ {
		if effective[i-1].Key >= effective[i].Key {
			t.Fatalf("output not sorted: %q before %q", effective[i-1].Key, effective[i].Key)
		}
	}
}

// An override naming a key the registry no longer defines must not appear in
// the resolved output at all — ResolveSettings answers "what does the
// platform's registry say", not "what rows exist in the table".
func TestResolveSettings_IgnoresOverridesForUndefinedKeys(t *testing.T) {
	overrides := []*Setting{
		{TenantID: "tn-1", Key: "a_retired_setting", Value: "whatever", RowVersion: 1},
	}
	effective := ResolveSettings(overrides)
	for _, es := range effective {
		if es.Key == "a_retired_setting" {
			t.Fatal("an override for an undefined key leaked into the resolved output")
		}
	}
	if len(effective) != len(SettingDefinitions) {
		t.Errorf("resolved %d settings, want exactly %d", len(effective), len(SettingDefinitions))
	}
}
