package domain

import (
	"errors"
	"testing"
)

func validRisk() *Risk {
	return &Risk{
		RiskID: "risk-1", TenantID: "tenant-a", Title: "Single vendor dependency",
		Description: "", Category: "operational",
		Likelihood: RiskLikelihoodPossible, Impact: RiskImpactModerate, Status: RiskOpen,
	}
}

// TestRiskLikelihood_Valid pins the closed five-member vocabulary.
func TestRiskLikelihood_Valid(t *testing.T) {
	tests := []struct {
		l    RiskLikelihood
		want bool
	}{
		{RiskLikelihoodRare, true},
		{RiskLikelihoodUnlikely, true},
		{RiskLikelihoodPossible, true},
		{RiskLikelihoodLikely, true},
		{RiskLikelihoodAlmostCertain, true},
		{"", false},
		{"Rare", false},
		{"bogus", false},
	}
	for _, tt := range tests {
		if got := tt.l.Valid(); got != tt.want {
			t.Errorf("RiskLikelihood(%q).Valid() = %v, want %v", tt.l, got, tt.want)
		}
	}
}

// TestRiskLikelihood_Rank pins the 1-based rank table
// RiskRepository.Register's doc comment states.
func TestRiskLikelihood_Rank(t *testing.T) {
	tests := []struct {
		l    RiskLikelihood
		want int
	}{
		{RiskLikelihoodRare, 1},
		{RiskLikelihoodUnlikely, 2},
		{RiskLikelihoodPossible, 3},
		{RiskLikelihoodLikely, 4},
		{RiskLikelihoodAlmostCertain, 5},
		{"bogus", 0},
	}
	for _, tt := range tests {
		if got := tt.l.Rank(); got != tt.want {
			t.Errorf("RiskLikelihood(%q).Rank() = %d, want %d", tt.l, got, tt.want)
		}
	}
}

// TestRiskImpact_Valid pins the closed five-member vocabulary.
func TestRiskImpact_Valid(t *testing.T) {
	tests := []struct {
		i    RiskImpact
		want bool
	}{
		{RiskImpactNegligible, true},
		{RiskImpactMinor, true},
		{RiskImpactModerate, true},
		{RiskImpactMajor, true},
		{RiskImpactSevere, true},
		{"", false},
		{"bogus", false},
	}
	for _, tt := range tests {
		if got := tt.i.Valid(); got != tt.want {
			t.Errorf("RiskImpact(%q).Valid() = %v, want %v", tt.i, got, tt.want)
		}
	}
}

// TestRiskImpact_Rank pins the 1-based rank table.
func TestRiskImpact_Rank(t *testing.T) {
	tests := []struct {
		i    RiskImpact
		want int
	}{
		{RiskImpactNegligible, 1},
		{RiskImpactMinor, 2},
		{RiskImpactModerate, 3},
		{RiskImpactMajor, 4},
		{RiskImpactSevere, 5},
		{"bogus", 0},
	}
	for _, tt := range tests {
		if got := tt.i.Rank(); got != tt.want {
			t.Errorf("RiskImpact(%q).Rank() = %d, want %d", tt.i, got, tt.want)
		}
	}
}

// TestRiskLikelihoodImpact_NeverZeroOut proves the story's explicit
// requirement at the RANK level: both scales start at 1, so no combination
// of a defined likelihood and a defined impact ever produces a zero score.
func TestRiskLikelihoodImpact_NeverZeroOut(t *testing.T) {
	likelihoods := []RiskLikelihood{
		RiskLikelihoodRare, RiskLikelihoodUnlikely, RiskLikelihoodPossible, RiskLikelihoodLikely, RiskLikelihoodAlmostCertain,
	}
	impacts := []RiskImpact{
		RiskImpactNegligible, RiskImpactMinor, RiskImpactModerate, RiskImpactMajor, RiskImpactSevere,
	}
	for _, l := range likelihoods {
		for _, i := range impacts {
			if score := l.Rank() * i.Rank(); score <= 0 {
				t.Errorf("score(%s, %s) = %d, want > 0", l, i, score)
			}
		}
	}
}

// TestRiskTreatment_Valid pins the closed, OPTIONAL disposition vocabulary.
func TestRiskTreatment_Valid(t *testing.T) {
	tests := []struct {
		tr   RiskTreatment
		want bool
	}{
		{RiskTreatmentMitigate, true},
		{RiskTreatmentAccept, true},
		{RiskTreatmentTransfer, true},
		{RiskTreatmentAvoid, true},
		{"", false},
		{"bogus", false},
	}
	for _, tt := range tests {
		if got := tt.tr.Valid(); got != tt.want {
			t.Errorf("RiskTreatment(%q).Valid() = %v, want %v", tt.tr, got, tt.want)
		}
	}
}

// TestRiskStatus_Valid pins the closed lifecycle vocabulary.
func TestRiskStatus_Valid(t *testing.T) {
	tests := []struct {
		s    RiskStatus
		want bool
	}{
		{RiskOpen, true},
		{RiskMitigating, true},
		{RiskAccepted, true},
		{RiskClosed, true},
		{"", false},
		{"Open", false},
		{"resolved", false},
	}
	for _, tt := range tests {
		if got := tt.s.Valid(); got != tt.want {
			t.Errorf("RiskStatus(%q).Valid() = %v, want %v", tt.s, got, tt.want)
		}
	}
}

// TestRiskStatus_OpenForRegister proves every status except closed counts
// toward the register.
func TestRiskStatus_OpenForRegister(t *testing.T) {
	tests := []struct {
		s    RiskStatus
		want bool
	}{
		{RiskOpen, true},
		{RiskMitigating, true},
		{RiskAccepted, true},
		{RiskClosed, false},
	}
	for _, tt := range tests {
		if got := tt.s.OpenForRegister(); got != tt.want {
			t.Errorf("RiskStatus(%q).OpenForRegister() = %v, want %v", tt.s, got, tt.want)
		}
	}
}

// TestRiskStatus_CanTransitionTo_LegalEdges pins every permitted move the
// story specifies, exactly.
func TestRiskStatus_CanTransitionTo_LegalEdges(t *testing.T) {
	cases := []struct{ from, to RiskStatus }{
		{RiskOpen, RiskMitigating},
		{RiskOpen, RiskAccepted},
		{RiskOpen, RiskClosed},
		{RiskMitigating, RiskAccepted},
		{RiskMitigating, RiskClosed},
		{RiskMitigating, RiskOpen},
		{RiskAccepted, RiskOpen},
		{RiskAccepted, RiskClosed},
		{RiskClosed, RiskOpen},
	}
	for _, c := range cases {
		if !c.from.CanTransitionTo(c.to) {
			t.Errorf("%s -> %s should be legal", c.from, c.to)
		}
	}
}

// THIS MUST BITE: every move not explicitly listed above is refused,
// including every self-transition and any shortcut across the lifecycle.
func TestRiskStatus_CanTransitionTo_RejectsIllegalEdges(t *testing.T) {
	cases := []struct{ from, to RiskStatus }{
		{RiskOpen, RiskOpen},
		{RiskMitigating, RiskMitigating},
		{RiskAccepted, RiskAccepted},
		{RiskClosed, RiskClosed},
		{RiskAccepted, RiskMitigating},
		{RiskClosed, RiskMitigating},
		{RiskClosed, RiskAccepted},
	}
	for _, c := range cases {
		if c.from.CanTransitionTo(c.to) {
			t.Errorf("%s -> %s should be illegal", c.from, c.to)
		}
	}
}

func TestNewRiskTransitionError_UnwrapsToErrInvalidTransition(t *testing.T) {
	err := NewRiskTransitionError(RiskAccepted, RiskMitigating)
	if !errors.Is(err, ErrInvalidTransition) {
		t.Error("must unwrap to ErrInvalidTransition")
	}
	var target *RiskTransitionError
	if !errors.As(err, &target) {
		t.Fatal("must be a *RiskTransitionError")
	}
	if target.From != RiskAccepted || target.To != RiskMitigating {
		t.Errorf("From/To = %s/%s, want accepted/mitigating", target.From, target.To)
	}
}

// TestRiskSeverityBandOf pins the four equal-width bands over [1, 25].
func TestRiskSeverityBandOf(t *testing.T) {
	cases := []struct {
		score int
		want  RiskSeverityBand
	}{
		{1, RiskSeverityBandLow},
		{6, RiskSeverityBandLow},
		{7, RiskSeverityBandMedium},
		{12, RiskSeverityBandMedium},
		{13, RiskSeverityBandHigh},
		{18, RiskSeverityBandHigh},
		{19, RiskSeverityBandCritical},
		{25, RiskSeverityBandCritical},
	}
	for _, c := range cases {
		if got := RiskSeverityBandOf(c.score); got != c.want {
			t.Errorf("RiskSeverityBandOf(%d) = %q, want %q", c.score, got, c.want)
		}
	}
}

func TestRiskSeverityBand_Valid(t *testing.T) {
	for _, b := range []RiskSeverityBand{
		RiskSeverityBandCritical, RiskSeverityBandHigh, RiskSeverityBandMedium, RiskSeverityBandLow,
	} {
		if !b.Valid() {
			t.Errorf("%q should be valid", b)
		}
	}
	if RiskSeverityBand("none").Valid() {
		t.Error(`"none" should not be a valid band label`)
	}
}

// TestNewRisk_BuildsAFreshOpenRisk mirrors NewVulnFinding/NewIncident's
// shape: server-minted id, always open.
func TestNewRisk_BuildsAFreshOpenRisk(t *testing.T) {
	r, err := NewRisk("tenant-a", "Single vendor dependency", "we rely on one supplier",
		"operational", RiskLikelihoodPossible, RiskImpactMajor, nil, nil)
	if err != nil {
		t.Fatalf("new risk: %v", err)
	}
	if r.RiskID == "" {
		t.Error("risk_id must be server-minted, not empty")
	}
	if r.Status != RiskOpen {
		t.Errorf("Status = %q, want %q", r.Status, RiskOpen)
	}
	if r.Treatment != nil {
		t.Errorf("Treatment = %v, want nil (not supplied)", r.Treatment)
	}
	if r.AssetID != nil {
		t.Errorf("AssetID = %v, want nil (not supplied)", r.AssetID)
	}
	if err := r.Validate(); err != nil {
		t.Errorf("a freshly constructed risk must validate: %v", err)
	}
}

func TestNewRisk_AcceptsOptionalTreatmentAndAssetID(t *testing.T) {
	treatment := RiskTreatmentMitigate
	assetID := "asset-1"
	r, err := NewRisk("tenant-a", "Unpatched host", "", "security",
		RiskLikelihoodLikely, RiskImpactSevere, &treatment, &assetID)
	if err != nil {
		t.Fatalf("new risk: %v", err)
	}
	if r.Treatment == nil || *r.Treatment != RiskTreatmentMitigate {
		t.Errorf("Treatment = %v, want mitigate", r.Treatment)
	}
	if r.AssetID == nil || *r.AssetID != "asset-1" {
		t.Errorf("AssetID = %v, want asset-1", r.AssetID)
	}
}

func TestRisk_ValidateRejectsBlankIdentifiers(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Risk)
		field  string
	}{
		{"risk_id", func(r *Risk) { r.RiskID = "" }, "risk_id"},
		{"tenant_id", func(r *Risk) { r.TenantID = "  " }, "tenant_id"},
		{"title", func(r *Risk) { r.Title = "  " }, "title"},
	}
	for _, tt := range tests {
		r := validRisk()
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

func TestRisk_ValidateTitleLength(t *testing.T) {
	tooLong := make([]byte, MaxRiskTitleLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	r := validRisk()
	r.Title = string(tooLong)
	ve, ok := AsValidation(r.Validate())
	if !ok || ve.Field != "title" {
		t.Errorf("Validate() = %v, want a title ValidationError", r.Validate())
	}
}

func TestRisk_ValidateDescriptionLength(t *testing.T) {
	tooLong := make([]byte, MaxRiskDescriptionLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	r := validRisk()
	r.Description = string(tooLong)
	ve, ok := AsValidation(r.Validate())
	if !ok || ve.Field != "description" {
		t.Errorf("Validate() = %v, want a description ValidationError", r.Validate())
	}
	r2 := validRisk()
	r2.Description = ""
	if err := r2.Validate(); err != nil {
		t.Errorf("empty description must validate: %v", err)
	}
}

// TestRisk_ValidateCategory proves Category is OPTIONAL (blank validates)
// but, when supplied, must be bounded lower-case snake_case.
func TestRisk_ValidateCategory(t *testing.T) {
	tooLong := make([]byte, MaxRiskCategoryLength+1)
	for i := range tooLong {
		tooLong[i] = 'a'
	}
	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{"blank", "", true},
		{"lower snake case", "operational", true},
		{"with underscore", "single_point_of_failure", true},
		{"uppercase", "Operational", false},
		{"space", "operational risk", false},
		{"leading digit", "1operational", false},
		{"too long", string(tooLong), false},
	}
	for _, tt := range tests {
		r := validRisk()
		r.Category = tt.value
		err := r.Validate()
		if tt.valid && err != nil {
			t.Errorf("%s: Validate() = %v, want nil", tt.name, err)
			continue
		}
		if !tt.valid {
			ve, ok := AsValidation(err)
			if !ok || ve.Field != "category" {
				t.Errorf("%s: Validate() = %v, want a category ValidationError", tt.name, err)
			}
		}
	}
}

func TestRisk_ValidateRejectsUnknownLikelihoodOrImpact(t *testing.T) {
	r := validRisk()
	r.Likelihood = "bogus"
	ve, ok := AsValidation(r.Validate())
	if !ok || ve.Field != "likelihood" {
		t.Errorf("Validate() = %v, want a likelihood ValidationError", r.Validate())
	}

	r2 := validRisk()
	r2.Impact = "bogus"
	ve2, ok := AsValidation(r2.Validate())
	if !ok || ve2.Field != "impact" {
		t.Errorf("Validate() = %v, want an impact ValidationError", r2.Validate())
	}
}

func TestRisk_ValidateRejectsUnknownStatus(t *testing.T) {
	r := validRisk()
	r.Status = "bogus"
	ve, ok := AsValidation(r.Validate())
	if !ok || ve.Field != "status" {
		t.Errorf("Validate() = %v, want a status ValidationError", r.Validate())
	}
}

func TestRisk_ValidateRejectsUnknownTreatment(t *testing.T) {
	r := validRisk()
	bad := RiskTreatment("bogus")
	r.Treatment = &bad
	ve, ok := AsValidation(r.Validate())
	if !ok || ve.Field != "treatment" {
		t.Errorf("Validate() = %v, want a treatment ValidationError", r.Validate())
	}
}

func TestRisk_ValidateRejectsBlankAssetIDWhenSupplied(t *testing.T) {
	r := validRisk()
	blank := "   "
	r.AssetID = &blank
	ve, ok := AsValidation(r.Validate())
	if !ok || ve.Field != "asset_id" {
		t.Errorf("Validate() = %v, want an asset_id ValidationError", r.Validate())
	}
}

func TestRisk_ValidateAcceptsEveryDefinedLikelihoodAndImpact(t *testing.T) {
	for _, l := range []RiskLikelihood{
		RiskLikelihoodRare, RiskLikelihoodUnlikely, RiskLikelihoodPossible, RiskLikelihoodLikely, RiskLikelihoodAlmostCertain,
	} {
		r := validRisk()
		r.Likelihood = l
		if err := r.Validate(); err != nil {
			t.Errorf("Likelihood=%q: Validate() = %v, want nil", l, err)
		}
	}
	for _, i := range []RiskImpact{
		RiskImpactNegligible, RiskImpactMinor, RiskImpactModerate, RiskImpactMajor, RiskImpactSevere,
	} {
		r := validRisk()
		r.Impact = i
		if err := r.Validate(); err != nil {
			t.Errorf("Impact=%q: Validate() = %v, want nil", i, err)
		}
	}
}
