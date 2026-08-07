package domain

import (
	"errors"
	"testing"
)

func validComplianceControl() *ComplianceControl {
	return &ComplianceControl{
		ControlID: "control-1", TenantID: "tenant-a", Framework: "SOC2", ControlRef: "CC6.1",
		Title: "Logical access controls", Description: "", Status: ComplianceControlNotImplemented,
	}
}

// TestComplianceControlStatus_Valid pins the closed four-member vocabulary.
func TestComplianceControlStatus_Valid(t *testing.T) {
	tests := []struct {
		s    ComplianceControlStatus
		want bool
	}{
		{ComplianceControlNotImplemented, true},
		{ComplianceControlInProgress, true},
		{ComplianceControlImplemented, true},
		{ComplianceControlNotApplicable, true},
		{"", false},
		{"NotImplemented", false},
		{"bogus", false},
	}
	for _, tt := range tests {
		if got := tt.s.Valid(); got != tt.want {
			t.Errorf("ComplianceControlStatus(%q).Valid() = %v, want %v", tt.s, got, tt.want)
		}
	}
}

// TestComplianceControlStatus_CanTransitionTo_LegalEdges pins every
// permitted move the story specifies, exactly.
func TestComplianceControlStatus_CanTransitionTo_LegalEdges(t *testing.T) {
	cases := []struct{ from, to ComplianceControlStatus }{
		{ComplianceControlNotImplemented, ComplianceControlInProgress},
		{ComplianceControlNotImplemented, ComplianceControlNotApplicable},
		{ComplianceControlInProgress, ComplianceControlImplemented},
		{ComplianceControlInProgress, ComplianceControlNotApplicable},
		{ComplianceControlInProgress, ComplianceControlNotImplemented},
		{ComplianceControlImplemented, ComplianceControlInProgress},
		{ComplianceControlImplemented, ComplianceControlNotImplemented},
		{ComplianceControlNotApplicable, ComplianceControlNotImplemented},
	}
	for _, c := range cases {
		if !c.from.CanTransitionTo(c.to) {
			t.Errorf("%s -> %s should be legal", c.from, c.to)
		}
	}
}

// THIS MUST BITE: every move not explicitly listed above is refused,
// including every self-transition and any shortcut across the lifecycle —
// in particular implemented -> not_applicable, which must step back through
// in_progress first (ComplianceControlImplemented's own doc comment).
func TestComplianceControlStatus_CanTransitionTo_RejectsIllegalEdges(t *testing.T) {
	cases := []struct{ from, to ComplianceControlStatus }{
		{ComplianceControlNotImplemented, ComplianceControlNotImplemented},
		{ComplianceControlInProgress, ComplianceControlInProgress},
		{ComplianceControlImplemented, ComplianceControlImplemented},
		{ComplianceControlNotApplicable, ComplianceControlNotApplicable},
		{ComplianceControlImplemented, ComplianceControlNotApplicable},
		{ComplianceControlNotApplicable, ComplianceControlInProgress},
		{ComplianceControlNotApplicable, ComplianceControlImplemented},
		{ComplianceControlNotImplemented, ComplianceControlImplemented},
	}
	for _, c := range cases {
		if c.from.CanTransitionTo(c.to) {
			t.Errorf("%s -> %s should be illegal", c.from, c.to)
		}
	}
}

func TestNewComplianceControlTransitionError_UnwrapsToErrInvalidTransition(t *testing.T) {
	err := NewComplianceControlTransitionError(ComplianceControlNotApplicable, ComplianceControlImplemented)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Error("must unwrap to ErrInvalidTransition")
	}
	var target *ComplianceControlTransitionError
	if !errors.As(err, &target) {
		t.Fatal("must be a *ComplianceControlTransitionError")
	}
	if target.From != ComplianceControlNotApplicable || target.To != ComplianceControlImplemented {
		t.Errorf("From/To = %s/%s, want not_applicable/implemented", target.From, target.To)
	}
}

// TestNewComplianceControl_BuildsAFreshNotImplementedControl mirrors
// NewRisk/NewIncident's shape: server-minted id, always not_implemented.
func TestNewComplianceControl_BuildsAFreshNotImplementedControl(t *testing.T) {
	c, err := NewComplianceControl("tenant-a", "SOC2", "CC6.1", "Logical access controls", "restrict access to production")
	if err != nil {
		t.Fatalf("new compliance control: %v", err)
	}
	if c.ControlID == "" {
		t.Error("control_id must be server-minted, not empty")
	}
	if c.Status != ComplianceControlNotImplemented {
		t.Errorf("Status = %q, want %q", c.Status, ComplianceControlNotImplemented)
	}
	if c.Framework != "SOC2" || c.ControlRef != "CC6.1" {
		t.Errorf("Framework/ControlRef = %q/%q, want SOC2/CC6.1", c.Framework, c.ControlRef)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("a freshly constructed control must validate: %v", err)
	}
}

func TestComplianceControl_ValidateRejectsBlankIdentifiers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ComplianceControl)
		field  string
	}{
		{"control_id", func(c *ComplianceControl) { c.ControlID = "" }, "control_id"},
		{"tenant_id", func(c *ComplianceControl) { c.TenantID = "  " }, "tenant_id"},
		{"framework", func(c *ComplianceControl) { c.Framework = "  " }, "framework"},
		{"control_ref", func(c *ComplianceControl) { c.ControlRef = "  " }, "control_ref"},
		{"title", func(c *ComplianceControl) { c.Title = "  " }, "title"},
	}
	for _, tt := range tests {
		c := validComplianceControl()
		tt.mutate(c)
		err := c.Validate()
		ve, ok := AsValidation(err)
		if !ok {
			t.Fatalf("%s: Validate() err = %v, want a *ValidationError", tt.name, err)
		}
		if ve.Field != tt.field {
			t.Errorf("%s: Validate() field = %q, want %q", tt.name, ve.Field, tt.field)
		}
	}
}

// TestComplianceControl_ValidateFramework proves the case-preserving,
// bounded charset rule — deliberately DIFFERENT from Risk.Category's
// lower-snake-case rule (see complianceControlFrameworkPattern's own doc
// comment).
func TestComplianceControl_ValidateFramework(t *testing.T) {
	tooLong := make([]byte, MaxComplianceControlFrameworkLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{"canonical uppercase", "SOC2", true},
		{"hyphenated", "PCI-DSS", true},
		{"underscored", "nist_800_53", true},
		{"leading digit", "2SOC", false},
		{"space", "SOC 2", false},
		{"too long", string(tooLong), false},
	}
	for _, tt := range tests {
		c := validComplianceControl()
		c.Framework = tt.value
		err := c.Validate()
		if tt.valid && err != nil {
			t.Errorf("%s: Validate() = %v, want nil", tt.name, err)
			continue
		}
		if !tt.valid {
			ve, ok := AsValidation(err)
			if !ok || ve.Field != "framework" {
				t.Errorf("%s: Validate() = %v, want a framework ValidationError", tt.name, err)
			}
		}
	}
}

// TestComplianceControl_ValidateControlRef proves the dotted-section-number
// charset (e.g. "CC6.1", "A.9.2.3") is accepted.
func TestComplianceControl_ValidateControlRef(t *testing.T) {
	tooLong := make([]byte, MaxComplianceControlRefLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{"soc2 style", "CC6.1", true},
		{"iso style", "A.9.2.3", true},
		{"pci style", "3.4.1", true},
		{"space", "CC 6.1", false},
		{"too long", string(tooLong), false},
	}
	for _, tt := range tests {
		c := validComplianceControl()
		c.ControlRef = tt.value
		err := c.Validate()
		if tt.valid && err != nil {
			t.Errorf("%s: Validate() = %v, want nil", tt.name, err)
			continue
		}
		if !tt.valid {
			ve, ok := AsValidation(err)
			if !ok || ve.Field != "control_ref" {
				t.Errorf("%s: Validate() = %v, want a control_ref ValidationError", tt.name, err)
			}
		}
	}
}

func TestComplianceControl_ValidateTitleLength(t *testing.T) {
	tooLong := make([]byte, MaxComplianceControlTitleLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	c := validComplianceControl()
	c.Title = string(tooLong)
	ve, ok := AsValidation(c.Validate())
	if !ok || ve.Field != "title" {
		t.Errorf("Validate() = %v, want a title ValidationError", c.Validate())
	}
}

func TestComplianceControl_ValidateDescriptionLength(t *testing.T) {
	tooLong := make([]byte, MaxComplianceControlDescriptionLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	c := validComplianceControl()
	c.Description = string(tooLong)
	ve, ok := AsValidation(c.Validate())
	if !ok || ve.Field != "description" {
		t.Errorf("Validate() = %v, want a description ValidationError", c.Validate())
	}
	c2 := validComplianceControl()
	c2.Description = ""
	if err := c2.Validate(); err != nil {
		t.Errorf("empty description must validate: %v", err)
	}
}

func TestComplianceControl_ValidateRejectsUnknownStatus(t *testing.T) {
	c := validComplianceControl()
	c.Status = "bogus"
	ve, ok := AsValidation(c.Validate())
	if !ok || ve.Field != "status" {
		t.Errorf("Validate() = %v, want a status ValidationError", c.Validate())
	}
}

// ---------------------------------------------------------------- ControlEvidenceKind

func TestControlEvidenceKind_Valid(t *testing.T) {
	tests := []struct {
		k    ControlEvidenceKind
		want bool
	}{
		{ControlEvidenceKindURL, true},
		{ControlEvidenceKindNote, true},
		{ControlEvidenceKindAttestation, true},
		{"", false},
		{"URL", false},
		{"bogus", false},
	}
	for _, tt := range tests {
		if got := tt.k.Valid(); got != tt.want {
			t.Errorf("ControlEvidenceKind(%q).Valid() = %v, want %v", tt.k, got, tt.want)
		}
	}
}

func validControlEvidence() *ControlEvidence {
	return &ControlEvidence{
		EvidenceID: "evidence-1", TenantID: "tenant-a", ControlID: "control-1",
		Kind: ControlEvidenceKindNote, Value: "quarterly access review completed", RecordedBy: "user-1",
	}
}

func TestNewControlEvidence_BuildsAFreshRecord(t *testing.T) {
	e, err := NewControlEvidence("tenant-a", "control-1", ControlEvidenceKindURL, "https://example.com/proof.pdf", "user-1")
	if err != nil {
		t.Fatalf("new control evidence: %v", err)
	}
	if e.EvidenceID == "" {
		t.Error("evidence_id must be server-minted, not empty")
	}
	if e.Kind != ControlEvidenceKindURL || e.Value != "https://example.com/proof.pdf" || e.RecordedBy != "user-1" {
		t.Errorf("evidence = %+v, want the supplied fields", e)
	}
	if err := e.Validate(); err != nil {
		t.Errorf("a freshly constructed evidence record must validate: %v", err)
	}
}

func TestControlEvidence_ValidateRejectsBlankIdentifiers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ControlEvidence)
		field  string
	}{
		{"evidence_id", func(e *ControlEvidence) { e.EvidenceID = "" }, "evidence_id"},
		{"tenant_id", func(e *ControlEvidence) { e.TenantID = "  " }, "tenant_id"},
		{"control_id", func(e *ControlEvidence) { e.ControlID = "  " }, "control_id"},
		{"recorded_by", func(e *ControlEvidence) { e.RecordedBy = "  " }, "recorded_by"},
	}
	for _, tt := range tests {
		e := validControlEvidence()
		tt.mutate(e)
		err := e.Validate()
		ve, ok := AsValidation(err)
		if !ok {
			t.Fatalf("%s: Validate() err = %v, want a *ValidationError", tt.name, err)
		}
		if ve.Field != tt.field {
			t.Errorf("%s: Validate() field = %q, want %q", tt.name, ve.Field, tt.field)
		}
	}
}

func TestControlEvidence_ValidateRejectsUnknownKind(t *testing.T) {
	e := validControlEvidence()
	e.Kind = "bogus"
	ve, ok := AsValidation(e.Validate())
	if !ok || ve.Field != "kind" {
		t.Errorf("Validate() = %v, want a kind ValidationError", e.Validate())
	}
}

func TestControlEvidence_ValidateRejectsBlankOrOversizedValue(t *testing.T) {
	blank := validControlEvidence()
	blank.Value = "   "
	ve, ok := AsValidation(blank.Validate())
	if !ok || ve.Field != "value" {
		t.Errorf("blank value: Validate() = %v, want a value ValidationError", blank.Validate())
	}

	tooLong := make([]byte, MaxControlEvidenceValueLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	oversized := validControlEvidence()
	oversized.Value = string(tooLong)
	ve2, ok := AsValidation(oversized.Validate())
	if !ok || ve2.Field != "value" {
		t.Errorf("oversized value: Validate() = %v, want a value ValidationError", oversized.Validate())
	}
}
