package domain

import "testing"

// §9.3 cross-dimension invariants. One positive and one negative per invariant
// that this function owns; the remainder are recorded as unenforceable here with
// the reason, so the coverage gap is asserted rather than assumed.

func baseObj() *ConfigObject {
	return &ConfigObject{
		Artifact: "A.md", Version: "1.0.0", Role: RoleReference,
		Lifecycle: LifecycleRatified, RetentionClass: RetentionWorkingMaterial,
		Authority: AuthorityActive, RetentionPolicy: "permanent",
	}
}

// §9.3-1: Retention == Current Baseline ⇒ Authority == Active.
func TestInvariant1_CurrentBaselineRequiresActive(t *testing.T) {
	// positive
	o := baseObj()
	o.RetentionClass, o.Authority = RetentionCurrentBaseline, AuthorityActive
	if err := o.Validate(); err != nil {
		t.Fatalf("current_baseline + active must be valid, got %v", err)
	}

	// negative — every non-active authority
	for _, au := range []Authority{AuthorityHistorical, AuthorityNonNormative} {
		o := baseObj()
		o.RetentionClass, o.Authority = RetentionCurrentBaseline, au
		if err := o.Validate(); err == nil {
			t.Errorf("current_baseline + %s must be rejected (§9.3-1)", au)
		}
	}
}

// Authority is computed (§6), so an absent value is not a violation. This was
// the behaviour before §9.3-1 was completed and it must be preserved.
func TestInvariant1_EmptyAuthorityIsNotAViolation(t *testing.T) {
	o := baseObj()
	o.RetentionClass, o.Authority = RetentionCurrentBaseline, ""
	if err := o.Validate(); err != nil {
		t.Fatalf("current_baseline + unset authority must remain valid (§6): %v", err)
	}
}

// §9.3-7 is structurally satisfied: each dimension is a single-valued field, so
// no object can express two values on one dimension. This test records that the
// guarantee comes from the type, not from Validate().
func TestInvariant7_StructurallySatisfied(t *testing.T) {
	o := baseObj()
	o.Authority = AuthorityHistorical // assignment replaces; it cannot accumulate
	if o.Authority != AuthorityHistorical {
		t.Fatal("a dimension field must hold exactly one value")
	}
}

// §9.3-4 is a prohibition on inference, not a rejection rule: Lifecycle ==
// Complete must constrain Authority in neither direction.
func TestInvariant4_CompleteDoesNotConstrainAuthority(t *testing.T) {
	for _, au := range []Authority{AuthorityActive, AuthorityNonNormative, AuthorityHistorical} {
		o := baseObj()
		o.Lifecycle, o.Authority = LifecycleComplete, au
		o.RetentionClass = RetentionWorkingMaterial
		if err := o.Validate(); err != nil {
			t.Errorf("complete + %s must not be rejected (§9.3-4): %v", au, err)
		}
	}
}

// §9.3-2 as interpreted by CI-1 Issue 1: Active ⇒ Lifecycle ∉ {Draft, Withdrawn}.
func TestInvariant2_ActiveExcludesDraftAndWithdrawn(t *testing.T) {
	// negative — the two barred states
	for _, lc := range []Lifecycle{LifecycleDraft, LifecycleWithdrawn} {
		o := baseObj()
		o.Authority, o.Lifecycle = AuthorityActive, lc
		if err := o.Validate(); err == nil {
			t.Errorf("active + %s must be rejected (§9.3-2, CI-1 Issue 1)", lc)
		}
	}

	// positive — every other lifecycle stays valid, including the two the
	// literal enumeration omitted. Suspended is the case that would have made
	// §8 Suspension inoperable.
	for _, lc := range []Lifecycle{
		LifecycleRatified, LifecycleApproved, LifecycleInProgress,
		LifecycleComplete, LifecycleDeprecated,
		LifecycleSuspended, LifecycleInReview,
	} {
		o := baseObj()
		o.Authority, o.Lifecycle = AuthorityActive, lc
		if err := o.Validate(); err != nil {
			t.Errorf("active + %s must remain valid: %v", lc, err)
		}
	}
}

// §9.3-5 as interpreted by CI-1 Issue 2: the three named archival classes bar
// Active; Audit Record does not.
func TestInvariant5_ArchivalRetentionExcludesActive(t *testing.T) {
	// negative — the three classes §9.3-5 names
	for _, rc := range []RetentionClass{
		RetentionHistoricalRecord, RetentionHistoricalEvidence, RetentionSupersededPlan,
	} {
		o := baseObj()
		o.Authority, o.RetentionClass = AuthorityActive, rc
		if err := o.Validate(); err == nil {
			t.Errorf("active + %s must be rejected (§9.3-5)", rc)
		}
	}

	// positive — Audit Record is outside the invariant, which is what lets §8
	// Archiving preserve Authority while archiving an Active artifact.
	o := baseObj()
	o.Authority, o.RetentionClass = AuthorityActive, RetentionAuditRecord
	if err := o.Validate(); err != nil {
		t.Fatalf("active + audit_record must be valid (CI-1 Issue 2): %v", err)
	}

	// positive — the same three classes are valid for non-Active authority.
	for _, rc := range []RetentionClass{
		RetentionHistoricalRecord, RetentionHistoricalEvidence, RetentionSupersededPlan,
	} {
		o := baseObj()
		o.Authority, o.Lifecycle, o.RetentionClass = AuthorityHistorical, LifecycleWithdrawn, rc
		if err := o.Validate(); err != nil {
			t.Errorf("historical + %s must be valid: %v", rc, err)
		}
	}
}

// --- INT-3 inception rule (CP-5.2) ------------------------------------------
//
// The rule lives in the domain so every caller observes it, not only HTTP.

func TestInception_AcceptsTheInceptionState(t *testing.T) {
	for _, lc := range []Lifecycle{LifecycleDraft, LifecycleInProgress} {
		o := baseObj()
		o.Lifecycle, o.RetentionClass, o.Authority = lc, RetentionWorkingMaterial, AuthorityNonNormative
		if err := o.ValidateInception(); err != nil {
			t.Errorf("%s + working_material + non_normative must be valid: %v", lc, err)
		}
	}

	// Authority unset is the normal case at creation — it is computed (§6).
	o := baseObj()
	o.Lifecycle, o.RetentionClass, o.Authority = LifecycleDraft, RetentionWorkingMaterial, ""
	if err := o.ValidateInception(); err != nil {
		t.Errorf("unset authority must be valid at inception: %v", err)
	}
}

func TestInception_RejectsNonInceptionLifecycle(t *testing.T) {
	for _, lc := range []Lifecycle{
		LifecycleRatified, LifecycleApproved, LifecycleInReview,
		LifecycleComplete, LifecycleSuspended, LifecycleDeprecated, LifecycleWithdrawn,
	} {
		o := baseObj()
		o.Lifecycle, o.RetentionClass, o.Authority = lc, RetentionWorkingMaterial, AuthorityNonNormative
		if err := o.ValidateInception(); err == nil {
			t.Errorf("lifecycle %s must be rejected at inception (INT-3)", lc)
		}
	}
}

func TestInception_RejectsNonWorkingMaterialRetention(t *testing.T) {
	for _, rc := range []RetentionClass{
		RetentionCurrentBaseline, RetentionCurrentPlanning, RetentionHistoricalRecord,
		RetentionSupersededPlan, RetentionHistoricalEvidence, RetentionAuditRecord,
	} {
		o := baseObj()
		o.Lifecycle, o.RetentionClass, o.Authority = LifecycleDraft, rc, AuthorityNonNormative
		if err := o.ValidateInception(); err == nil {
			t.Errorf("retention %s must be rejected at inception (INT-3)", rc)
		}
	}
}

// The reason the rule had to leave transport: an in-process caller constructing
// a ConfigObject directly is now bound by it too.
func TestInception_RejectsAssertedAuthority(t *testing.T) {
	for _, au := range []Authority{AuthorityActive, AuthorityHistorical} {
		o := baseObj()
		o.Lifecycle, o.RetentionClass, o.Authority = LifecycleDraft, RetentionWorkingMaterial, au
		if err := o.ValidateInception(); err == nil {
			t.Errorf("authority %s must be rejected at inception (INT-3, §6)", au)
		}
	}
}

// Inception must NOT be folded into Validate(): §8 transitions legitimately
// produce ratified / current_baseline / active, and governance.Execute validates
// their result. If these ever failed, Ratification would become impossible.
func TestInception_IsSeparateFromValidate(t *testing.T) {
	o := baseObj()
	o.Lifecycle, o.RetentionClass, o.Authority = LifecycleRatified, RetentionCurrentBaseline, AuthorityActive
	if err := o.Validate(); err != nil {
		t.Fatalf("a ratified baseline object must pass Validate(): %v", err)
	}
	if err := o.ValidateInception(); err == nil {
		t.Fatal("the same object must fail ValidateInception() — the two rules are distinct")
	}
}
