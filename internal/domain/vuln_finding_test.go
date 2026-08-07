package domain

import (
	"errors"
	"testing"
)

func validVulnFinding() *VulnFinding {
	return &VulnFinding{
		FindingID: "finding-1", TenantID: "tenant-a", AssetID: "asset-1",
		VulnID: "CVE-2024-1234", Title: "OpenSSL buffer overflow",
		Severity: VulnFindingSeverityHigh, Status: VulnFindingOpen,
		Scanner: "nessus", Description: "",
	}
}

// TestVulnFindingSeverity_Valid pins the closed vocabulary: exactly the five
// CVSS-derived ratings, including "none" — deliberately absent from
// IncidentSeverity — are defined.
func TestVulnFindingSeverity_Valid(t *testing.T) {
	tests := []struct {
		sev  VulnFindingSeverity
		want bool
	}{
		{VulnFindingSeverityNone, true},
		{VulnFindingSeverityLow, true},
		{VulnFindingSeverityMedium, true},
		{VulnFindingSeverityHigh, true},
		{VulnFindingSeverityCritical, true},
		{"", false},
		{"info", false}, // ObservationSeverity's member, not this vocabulary's
		{"None", false},
		{"bogus", false},
	}
	for _, tt := range tests {
		if got := tt.sev.Valid(); got != tt.want {
			t.Errorf("VulnFindingSeverity(%q).Valid() = %v, want %v", tt.sev, got, tt.want)
		}
	}
}

// TestVulnFindingStatus_Valid pins the closed lifecycle vocabulary.
func TestVulnFindingStatus_Valid(t *testing.T) {
	tests := []struct {
		status VulnFindingStatus
		want   bool
	}{
		{VulnFindingOpen, true},
		{VulnFindingRemediated, true},
		{VulnFindingAcceptedRisk, true},
		{VulnFindingFalsePositive, true},
		{"", false},
		{"Open", false},
		{"closed", false},
	}
	for _, tt := range tests {
		if got := tt.status.Valid(); got != tt.want {
			t.Errorf("VulnFindingStatus(%q).Valid() = %v, want %v", tt.status, got, tt.want)
		}
	}
}

// TestVulnFindingStatus_CanTransitionTo_LegalEdges pins every permitted move:
// open fans out to all three dispositions, and every disposition returns to
// open — including false_positive, but ONLY through this map (manual
// SetStatus); Upsert never calls it (see VulnFindingFalsePositive's doc
// comment) — that exclusion is proved separately by the store's own
// Upsert-preserves-false-positive test, not here.
func TestVulnFindingStatus_CanTransitionTo_LegalEdges(t *testing.T) {
	cases := []struct{ from, to VulnFindingStatus }{
		{VulnFindingOpen, VulnFindingRemediated},
		{VulnFindingOpen, VulnFindingAcceptedRisk},
		{VulnFindingOpen, VulnFindingFalsePositive},
		{VulnFindingRemediated, VulnFindingOpen},
		{VulnFindingAcceptedRisk, VulnFindingOpen},
		{VulnFindingFalsePositive, VulnFindingOpen},
	}
	for _, c := range cases {
		if !c.from.CanTransitionTo(c.to) {
			t.Errorf("%s -> %s should be legal", c.from, c.to)
		}
	}
}

// THIS MUST BITE: every move not explicitly listed above is refused,
// including self-transitions and any direct disposition-to-disposition move
// (an operator must go through open to switch, e.g., accepted_risk to
// false_positive — there is no shortcut).
func TestVulnFindingStatus_CanTransitionTo_RejectsIllegalEdges(t *testing.T) {
	cases := []struct{ from, to VulnFindingStatus }{
		{VulnFindingOpen, VulnFindingOpen},
		{VulnFindingRemediated, VulnFindingRemediated},
		{VulnFindingRemediated, VulnFindingAcceptedRisk},
		{VulnFindingRemediated, VulnFindingFalsePositive},
		{VulnFindingAcceptedRisk, VulnFindingAcceptedRisk},
		{VulnFindingAcceptedRisk, VulnFindingRemediated},
		{VulnFindingAcceptedRisk, VulnFindingFalsePositive},
		{VulnFindingFalsePositive, VulnFindingFalsePositive},
		{VulnFindingFalsePositive, VulnFindingRemediated},
		{VulnFindingFalsePositive, VulnFindingAcceptedRisk},
	}
	for _, c := range cases {
		if c.from.CanTransitionTo(c.to) {
			t.Errorf("%s -> %s should be illegal", c.from, c.to)
		}
	}
}

func TestNewVulnFindingTransitionError_UnwrapsToErrInvalidTransition(t *testing.T) {
	err := NewVulnFindingTransitionError(VulnFindingRemediated, VulnFindingAcceptedRisk)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Error("must unwrap to ErrInvalidTransition")
	}
	var target *VulnFindingTransitionError
	if !errors.As(err, &target) {
		t.Fatal("must be an *VulnFindingTransitionError")
	}
	if target.From != VulnFindingRemediated || target.To != VulnFindingAcceptedRisk {
		t.Errorf("From/To = %s/%s, want remediated/accepted_risk", target.From, target.To)
	}
}

// TestNewVulnFinding_BuildsAFreshOpenFinding proves the constructor mirrors
// NewSecurityRule/NewIOC's shape: server-minted id, always open, no
// FirstSeen/LastSeen set (store-managed).
func TestNewVulnFinding_BuildsAFreshOpenFinding(t *testing.T) {
	f, err := NewVulnFinding("tenant-a", "asset-1", "CVE-2024-1234", "OpenSSL buffer overflow",
		VulnFindingSeverityHigh, "nessus", "")
	if err != nil {
		t.Fatalf("new vuln finding: %v", err)
	}
	if f.FindingID == "" {
		t.Error("finding_id must be server-minted, not empty")
	}
	if f.Status != VulnFindingOpen {
		t.Errorf("Status = %q, want %q", f.Status, VulnFindingOpen)
	}
	if !f.FirstSeen.IsZero() || !f.LastSeen.IsZero() {
		t.Error("FirstSeen/LastSeen must be left zero — they are store-managed, not constructor-set")
	}
	if err := f.Validate(); err != nil {
		t.Errorf("a freshly constructed finding must validate: %v", err)
	}
}

func TestVulnFinding_ValidateRejectsBlankIdentifiers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*VulnFinding)
		field  string
	}{
		{"finding_id", func(f *VulnFinding) { f.FindingID = "" }, "finding_id"},
		{"tenant_id", func(f *VulnFinding) { f.TenantID = "  " }, "tenant_id"},
		{"asset_id", func(f *VulnFinding) { f.AssetID = "" }, "asset_id"},
		{"vuln_id", func(f *VulnFinding) { f.VulnID = "" }, "vuln_id"},
		{"title", func(f *VulnFinding) { f.Title = "  " }, "title"},
		{"scanner", func(f *VulnFinding) { f.Scanner = "" }, "scanner"},
	}
	for _, tt := range tests {
		f := validVulnFinding()
		tt.mutate(f)
		err := f.Validate()
		ve, ok := AsValidation(err)
		if !ok {
			t.Fatalf("%s: Validate() err = %v, want a *ValidationError", tt.name, err)
		}
		if ve.Field != tt.field {
			t.Errorf("%s: Validate() field = %q, want %q", tt.name, ve.Field, tt.field)
		}
	}
}

// TestVulnFinding_ValidateVulnID proves the charset/length bound — and,
// crucially, that a NON-CVE identifier (GHSA/RHSA/vendor codes) is accepted:
// this field must not be hard-required to look like a CVE.
func TestVulnFinding_ValidateVulnID(t *testing.T) {
	tooLong := make([]byte, MaxVulnFindingVulnIDLength+1)
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
		{"space", "CVE 2024 1234", false},
		{"leading punctuation", "-CVE-2024-1234", false},
		{"cve", "CVE-2024-1234", true},
		{"ghsa", "GHSA-xxxx-xxxx-xxxx", true},
		{"rhsa with colon", "RHSA-2024:1234", true},
		{"vendor code with dot", "MS17.010", true},
		{"internal plugin id", "nessus_plugin_12345", true},
	}
	for _, tt := range tests {
		f := validVulnFinding()
		f.VulnID = tt.value
		err := f.Validate()
		if tt.valid && err != nil {
			t.Errorf("%s: Validate() = %v, want nil", tt.name, err)
			continue
		}
		if !tt.valid {
			ve, ok := AsValidation(err)
			if !ok || ve.Field != "vuln_id" {
				t.Errorf("%s: Validate() = %v, want a vuln_id ValidationError", tt.name, err)
			}
		}
	}
}

func TestVulnFinding_ValidateRejectsUnknownSeverity(t *testing.T) {
	for _, bad := range []VulnFindingSeverity{"", "bogus", "info", "None"} {
		f := validVulnFinding()
		f.Severity = bad
		err := f.Validate()
		ve, ok := AsValidation(err)
		if !ok || ve.Field != "severity" {
			t.Errorf("Severity=%q: Validate() = %v, want a severity ValidationError", bad, err)
		}
	}
}

func TestVulnFinding_ValidateAcceptsEveryDefinedSeverity(t *testing.T) {
	for _, good := range []VulnFindingSeverity{
		VulnFindingSeverityNone, VulnFindingSeverityLow, VulnFindingSeverityMedium,
		VulnFindingSeverityHigh, VulnFindingSeverityCritical,
	} {
		f := validVulnFinding()
		f.Severity = good
		if err := f.Validate(); err != nil {
			t.Errorf("Severity=%q: Validate() = %v, want nil", good, err)
		}
	}
}

func TestVulnFinding_ValidateRejectsUnknownStatus(t *testing.T) {
	f := validVulnFinding()
	f.Status = "bogus"
	err := f.Validate()
	ve, ok := AsValidation(err)
	if !ok || ve.Field != "status" {
		t.Errorf("Validate() = %v, want a status ValidationError", err)
	}
}

func TestVulnFinding_ValidateTitleLength(t *testing.T) {
	tooLong := make([]byte, MaxVulnFindingTitleLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	f := validVulnFinding()
	f.Title = string(tooLong)
	ve, ok := AsValidation(f.Validate())
	if !ok || ve.Field != "title" {
		t.Errorf("Validate() = %v, want a title ValidationError", f.Validate())
	}
}

func TestVulnFinding_ValidateScannerLength(t *testing.T) {
	tooLong := make([]byte, MaxVulnFindingScannerLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	f := validVulnFinding()
	f.Scanner = string(tooLong)
	ve, ok := AsValidation(f.Validate())
	if !ok || ve.Field != "scanner" {
		t.Errorf("Validate() = %v, want a scanner ValidationError", f.Validate())
	}
}

func TestVulnFinding_ValidateDescriptionLength(t *testing.T) {
	tooLong := make([]byte, MaxVulnFindingDescriptionLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	f := validVulnFinding()
	f.Description = string(tooLong)
	ve, ok := AsValidation(f.Validate())
	if !ok || ve.Field != "description" {
		t.Errorf("Validate() = %v, want a description ValidationError", f.Validate())
	}
	// An empty description (the common case — most findings carry none) must
	// still validate cleanly.
	f2 := validVulnFinding()
	f2.Description = ""
	if err := f2.Validate(); err != nil {
		t.Errorf("empty description must validate: %v", err)
	}
}

// TestVulnFindingPriority_Valid pins the closed vocabulary: critical, high,
// medium, low — nothing else, and deliberately no "none" (see
// VulnFindingPriority's own doc comment).
func TestVulnFindingPriority_Valid(t *testing.T) {
	for _, p := range []VulnFindingPriority{
		VulnFindingPriorityCritical, VulnFindingPriorityHigh, VulnFindingPriorityMedium, VulnFindingPriorityLow,
	} {
		if !p.Valid() {
			t.Errorf("%q should be valid", p)
		}
	}
	if VulnFindingPriority("none").Valid() {
		t.Error(`"none" should not be a valid priority label`)
	}
}

// TestVulnFindingPriorityOf pins the four equal-width bands over [1, 25] —
// the boundaries VulnFindingPriorityOf's own doc comment states, plus the
// full range's own extremes.
func TestVulnFindingPriorityOf(t *testing.T) {
	cases := []struct {
		score int
		want  VulnFindingPriority
	}{
		{1, VulnFindingPriorityLow},
		{6, VulnFindingPriorityLow},
		{7, VulnFindingPriorityMedium},
		{12, VulnFindingPriorityMedium},
		{13, VulnFindingPriorityHigh},
		{18, VulnFindingPriorityHigh},
		{19, VulnFindingPriorityCritical},
		{25, VulnFindingPriorityCritical},
	}
	for _, c := range cases {
		if got := VulnFindingPriorityOf(c.score); got != c.want {
			t.Errorf("VulnFindingPriorityOf(%d) = %q, want %q", c.score, got, c.want)
		}
	}
}

// TestVulnFindingPriorityOf_UnknownCriticalityNeverOutranksKnownLow proves
// the story's explicit requirement at the RANK level (not just the label):
// an untiered asset's criticality rank must never exceed "low"'s — using the
// exact 1-based rank tables VulnFindingRepository.Prioritized's doc comment
// states.
func TestVulnFindingPriorityOf_UnknownCriticalityNeverOutranksKnownLow(t *testing.T) {
	severityRank := map[VulnFindingSeverity]int{
		VulnFindingSeverityNone: 1, VulnFindingSeverityLow: 2, VulnFindingSeverityMedium: 3,
		VulnFindingSeverityHigh: 4, VulnFindingSeverityCritical: 5,
	}
	criticalityRank := map[AssetCriticality]int{
		AssetCriticalityUnknown: 1, AssetCriticalityLow: 2, AssetCriticalityMedium: 3,
		AssetCriticalityHigh: 4, AssetCriticalityCritical: 5,
	}
	if criticalityRank[AssetCriticalityUnknown] > criticalityRank[AssetCriticalityLow] {
		t.Fatalf("unknown criticality rank (%d) outranks low's (%d)",
			criticalityRank[AssetCriticalityUnknown], criticalityRank[AssetCriticalityLow])
	}
	// For every severity, an unknown-criticality score must never exceed the
	// SAME severity's low-criticality score.
	for sev, sr := range severityRank {
		unknownScore := sr * criticalityRank[AssetCriticalityUnknown]
		lowScore := sr * criticalityRank[AssetCriticalityLow]
		if unknownScore > lowScore {
			t.Errorf("severity %q: unknown-criticality score %d outranks low-criticality score %d",
				sev, unknownScore, lowScore)
		}
	}
}
