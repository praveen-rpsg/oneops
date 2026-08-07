package domain

import "testing"

func validIOC() *IOC {
	return &IOC{
		IOCID: "ioc-1", TenantID: "tenant-a",
		IndicatorType: IOCIndicatorTypeIP, IndicatorValue: "203.0.113.5",
		Severity: IncidentSeverityHigh, Enabled: true,
	}
}

// TestIOCIndicatorType_Valid pins the enum: exactly the five defined kinds
// validate; everything else — including the empty string and a
// differently-cased spelling — does not.
func TestIOCIndicatorType_Valid(t *testing.T) {
	tests := []struct {
		typ  IOCIndicatorType
		want bool
	}{
		{IOCIndicatorTypeIP, true},
		{IOCIndicatorTypeDomain, true},
		{IOCIndicatorTypeURL, true},
		{IOCIndicatorTypeFileHash, true},
		{IOCIndicatorTypeEmail, true},
		{"", false},
		{"IP", false},
		{"hash", false},
		{"bogus", false},
	}
	for _, tt := range tests {
		if got := tt.typ.Valid(); got != tt.want {
			t.Errorf("IOCIndicatorType(%q).Valid() = %v, want %v", tt.typ, got, tt.want)
		}
	}
}

// TestNormalizeIOCIndicatorValue_LowersCaseInsensitiveTypesOnly proves
// domain/url/email are lower-cased and trimmed, while ip/file_hash are only
// trimmed — the split NormalizeIOCIndicatorValue's own doc comment states.
func TestNormalizeIOCIndicatorValue_LowersCaseInsensitiveTypesOnly(t *testing.T) {
	tests := []struct {
		name string
		typ  IOCIndicatorType
		in   string
		want string
	}{
		{"domain lower-cased", IOCIndicatorTypeDomain, "  Evil.Example.COM  ", "evil.example.com"},
		{"url lower-cased", IOCIndicatorTypeURL, " HTTP://Evil.Example/Path ", "http://evil.example/path"},
		{"email lower-cased", IOCIndicatorTypeEmail, " Attacker@Evil.EXAMPLE ", "attacker@evil.example"},
		{"ip trimmed only", IOCIndicatorTypeIP, "  203.0.113.5  ", "203.0.113.5"},
		{"file_hash trimmed only, case preserved", IOCIndicatorTypeFileHash, "  ABCDEF0123  ", "ABCDEF0123"},
	}
	for _, tt := range tests {
		if got := NormalizeIOCIndicatorValue(tt.typ, tt.in); got != tt.want {
			t.Errorf("%s: NormalizeIOCIndicatorValue(%q, %q) = %q, want %q", tt.name, tt.typ, tt.in, got, tt.want)
		}
	}
}

// TestNewIOC_DefaultsAndValidates proves the constructor mirrors
// NewSecurityRule's shape: a fresh entry starts enabled, with no
// description/source, and its indicator_value is normalized even though the
// constructor takes the raw, unnormalized form.
func TestNewIOC_DefaultsAndValidates(t *testing.T) {
	i, err := NewIOC("tenant-a", IOCIndicatorTypeDomain, "  Evil.Example.COM  ", IncidentSeverityCritical)
	if err != nil {
		t.Fatalf("new ioc: %v", err)
	}
	if i.IOCID == "" {
		t.Error("ioc_id must be server-minted, not empty")
	}
	if !i.Enabled {
		t.Error("a freshly constructed ioc must be enabled")
	}
	if i.IndicatorValue != "evil.example.com" {
		t.Errorf("IndicatorValue = %q, want normalized %q", i.IndicatorValue, "evil.example.com")
	}
	if i.Description != "" || i.Source != "" {
		t.Errorf("a freshly constructed ioc must have no description/source: %+v", i)
	}
	if err := i.Validate(); err != nil {
		t.Errorf("a freshly constructed ioc must validate: %v", err)
	}
}

// TestIOC_ValidateRejectsBlankIdentifiers proves the same isolation-key
// discipline SecurityRule.Validate enforces: ioc_id/tenant_id must not be
// empty.
func TestIOC_ValidateRejectsBlankIdentifiers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*IOC)
		field  string
	}{
		{"ioc_id", func(i *IOC) { i.IOCID = "" }, "ioc_id"},
		{"tenant_id", func(i *IOC) { i.TenantID = "  " }, "tenant_id"},
	}
	for _, tt := range tests {
		i := validIOC()
		tt.mutate(i)
		err := i.Validate()
		ve, ok := AsValidation(err)
		if !ok {
			t.Fatalf("%s: Validate() err = %v, want a *ValidationError", tt.name, err)
		}
		if ve.Field != tt.field {
			t.Errorf("%s: Validate() field = %q, want %q", tt.name, ve.Field, tt.field)
		}
	}
}

// TestIOC_ValidateRejectsUnknownIndicatorType is the mutation-provable bite:
// an out-of-enum indicator_type fails Validate with a field-level reason
// naming "indicator_type".
func TestIOC_ValidateRejectsUnknownIndicatorType(t *testing.T) {
	for _, bad := range []IOCIndicatorType{"", "bogus", "IP", "hash"} {
		i := validIOC()
		i.IndicatorType = bad
		err := i.Validate()
		ve, ok := AsValidation(err)
		if !ok {
			t.Fatalf("IndicatorType=%q: Validate() err = %v, want a *ValidationError", bad, err)
		}
		if ve.Field != "indicator_type" {
			t.Errorf("IndicatorType=%q: Validate() field = %q, want %q", bad, ve.Field, "indicator_type")
		}
	}
}

// TestIOC_ValidateAcceptsEveryDefinedIndicatorType proves every member of
// IOCIndicatorType validates cleanly.
func TestIOC_ValidateAcceptsEveryDefinedIndicatorType(t *testing.T) {
	for _, good := range []IOCIndicatorType{
		IOCIndicatorTypeIP, IOCIndicatorTypeDomain, IOCIndicatorTypeURL, IOCIndicatorTypeFileHash, IOCIndicatorTypeEmail,
	} {
		i := validIOC()
		i.IndicatorType = good
		i.IndicatorValue = NormalizeIOCIndicatorValue(good, "some-value")
		if err := i.Validate(); err != nil {
			t.Errorf("IndicatorType=%q: Validate() = %v, want nil", good, err)
		}
	}
}

// TestIOC_ValidateRejectsEmptyOrTooLongIndicatorValue pins the
// non-empty/bounded discipline on indicator_value.
func TestIOC_ValidateRejectsEmptyOrTooLongIndicatorValue(t *testing.T) {
	tooLong := make([]byte, MaxIOCIndicatorValueLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{"empty", "", false},
		{"too long", string(tooLong), false},
		{"at max", string(tooLong[:MaxIOCIndicatorValueLength]), true},
		{"normal", "203.0.113.5", true},
	}
	for _, tt := range tests {
		i := validIOC()
		i.IndicatorValue = tt.value
		err := i.Validate()
		if tt.valid && err != nil {
			t.Errorf("%s: Validate() = %v, want nil", tt.name, err)
			continue
		}
		if !tt.valid {
			ve, ok := AsValidation(err)
			if !ok || ve.Field != "indicator_value" {
				t.Errorf("%s: Validate() = %v, want an indicator_value ValidationError", tt.name, err)
			}
		}
	}
}

// TestIOC_ValidateRejectsUnnormalizedIndicatorValue proves Validate is a
// defense-in-depth check, not just something NewIOC happens to satisfy: a
// hand-built IOC whose IndicatorValue is not already in its type's
// normalized form is rejected — the same reason SecurityRule.Validate
// rejects an unnormalized ObservationType.
func TestIOC_ValidateRejectsUnnormalizedIndicatorValue(t *testing.T) {
	tests := []struct {
		name  string
		typ   IOCIndicatorType
		value string
	}{
		{"domain uppercase", IOCIndicatorTypeDomain, "Evil.Example.COM"},
		{"domain untrimmed", IOCIndicatorTypeDomain, " evil.example.com"},
		{"email uppercase", IOCIndicatorTypeEmail, "Attacker@Evil.example"},
		{"ip untrimmed", IOCIndicatorTypeIP, " 203.0.113.5"},
	}
	for _, tt := range tests {
		i := validIOC()
		i.IndicatorType = tt.typ
		i.IndicatorValue = tt.value
		err := i.Validate()
		ve, ok := AsValidation(err)
		if !ok || ve.Field != "indicator_value" {
			t.Errorf("%s: Validate() = %v, want an indicator_value ValidationError", tt.name, err)
		}
	}
}

// TestIOC_ValidateRejectsUnknownSeverity proves severity is validated
// against the INCIDENT severity vocabulary (critical/high/medium/low), not
// ObservationSeverity's five-level scale — "info" must be rejected.
func TestIOC_ValidateRejectsUnknownSeverity(t *testing.T) {
	for _, bad := range []IncidentSeverity{"", "bogus", "info", "warning"} {
		i := validIOC()
		i.Severity = bad
		err := i.Validate()
		ve, ok := AsValidation(err)
		if !ok {
			t.Fatalf("Severity=%q: Validate() err = %v, want a *ValidationError", bad, err)
		}
		if ve.Field != "severity" {
			t.Errorf("Severity=%q: Validate() field = %q, want %q", bad, ve.Field, "severity")
		}
	}
}

// TestIOC_ValidateAcceptsEveryDefinedSeverity proves every member of
// IncidentSeverity validates cleanly.
func TestIOC_ValidateAcceptsEveryDefinedSeverity(t *testing.T) {
	for _, good := range []IncidentSeverity{
		IncidentSeverityCritical, IncidentSeverityHigh, IncidentSeverityMedium, IncidentSeverityLow,
	} {
		i := validIOC()
		i.Severity = good
		if err := i.Validate(); err != nil {
			t.Errorf("Severity=%q: Validate() = %v, want nil", good, err)
		}
	}
}

// TestIOC_ValidateBoundsDescriptionAndSource pins the length bounds on the
// two open, operator-authored text fields.
func TestIOC_ValidateBoundsDescriptionAndSource(t *testing.T) {
	tooLongDesc := make([]byte, MaxIOCDescriptionLength+1)
	tooLongSrc := make([]byte, MaxIOCSourceLength+1)

	i := validIOC()
	i.Description = string(tooLongDesc)
	if ve, ok := AsValidation(i.Validate()); !ok || ve.Field != "description" {
		t.Errorf("too-long description: Validate() = %v, want a description ValidationError", i.Validate())
	}

	i = validIOC()
	i.Source = string(tooLongSrc)
	if ve, ok := AsValidation(i.Validate()); !ok || ve.Field != "source" {
		t.Errorf("too-long source: Validate() = %v, want a source ValidationError", i.Validate())
	}
}

// TestIOCPatch_FieldsArePointers confirms the patch shape mirrors
// SecurityRulePatch's: a zero-value patch leaves everything unchanged,
// distinguishing "not supplied" from "explicitly set to the zero value".
func TestIOCPatch_FieldsArePointers(t *testing.T) {
	var patch IOCPatch
	if patch.Severity != nil || patch.Enabled != nil || patch.Description != nil || patch.Source != nil {
		t.Fatal("a zero-value IOCPatch must leave every field untouched")
	}
	sev := IncidentSeverityLow
	patch.Severity = &sev
	if *patch.Severity != IncidentSeverityLow {
		t.Errorf("patch.Severity = %q, want %q", *patch.Severity, IncidentSeverityLow)
	}
}
