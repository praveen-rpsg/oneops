package domain

import "testing"

func TestRoleValid(t *testing.T) {
	for _, r := range []Role{
		RoleConstitution, RoleGovernance, RoleEngSpec, RoleValidation,
		RoleEvidence, RoleAudit, RolePlanning, RoleReference, RoleWorking,
	} {
		if !r.Valid() {
			t.Errorf("role %q should be valid", r)
		}
	}
	if Role("bogus").Valid() {
		t.Error("bogus role should be invalid")
	}
}

func TestLifecycleValid(t *testing.T) {
	for _, l := range []Lifecycle{
		LifecycleDraft, LifecycleInReview, LifecycleRatified, LifecycleApproved,
		LifecycleInProgress, LifecycleComplete, LifecycleSuspended,
		LifecycleDeprecated, LifecycleWithdrawn,
	} {
		if !l.Valid() {
			t.Errorf("lifecycle %q should be valid", l)
		}
	}
	if Lifecycle("bogus").Valid() {
		t.Error("bogus lifecycle should be invalid")
	}
}

func TestAuthorityValid(t *testing.T) {
	for _, a := range []Authority{AuthorityActive, AuthorityHistorical, AuthorityNonNormative} {
		if !a.Valid() {
			t.Errorf("authority %q should be valid", a)
		}
	}
	if Authority("bogus").Valid() {
		t.Error("bogus authority should be invalid")
	}
}

func TestRetentionClassValid(t *testing.T) {
	for _, rc := range []RetentionClass{
		RetentionCurrentBaseline, RetentionCurrentPlanning, RetentionHistoricalRecord,
		RetentionSupersededPlan, RetentionHistoricalEvidence, RetentionAuditRecord,
		RetentionWorkingMaterial,
	} {
		if !rc.Valid() {
			t.Errorf("retention class %q should be valid", rc)
		}
	}
	if RetentionClass("bogus").Valid() {
		t.Error("bogus retention class should be invalid")
	}
}

func TestFormatValid(t *testing.T) {
	for _, f := range []Format{FormatMarkdown, FormatMDX, FormatJSON} {
		if !f.Valid() {
			t.Errorf("format %q should be valid", f)
		}
	}
	if Format("bogus").Valid() {
		t.Error("bogus format should be invalid")
	}
}

// ---- §8 deletion protection ------------------------------------------------
//
// Configuration State Model §8: Constitution/Governance/Validation/Evidence/
// Audit roles are never deletable, regardless of any other dimension. The
// prohibition is on the role alone, so it must hold for every role in the
// enum, not merely the ones the current slice happens to list.

func TestProtectedFromDeletion_ExactlyTheFiveConstitutionalRoles(t *testing.T) {
	protected := map[Role]bool{
		RoleConstitution: true, RoleGovernance: true, RoleValidation: true,
		RoleEvidence: true, RoleAudit: true,
	}
	all := []Role{
		RoleConstitution, RoleGovernance, RoleEngSpec, RoleValidation,
		RoleEvidence, RoleAudit, RolePlanning, RoleReference, RoleWorking,
	}
	for _, r := range all {
		want := protected[r]
		if got := r.ProtectedFromDeletion(); got != want {
			t.Errorf("Role(%q).ProtectedFromDeletion() = %v, want %v (§8)", r, got, want)
		}
	}
}

func TestProtectedFromDeletion_UnknownRoleIsNotProtected(t *testing.T) {
	// An unrecognised role must not accidentally fall into the protected set —
	// the check is a membership test, not a default-deny on unknown input, so
	// this pins that the zero value/garbage input does not spuriously block
	// deletion (a different failure mode from the security-relevant one, but
	// one that would silently mask a real defect if it flipped).
	if Role("bogus").ProtectedFromDeletion() {
		t.Error("an unknown role must not be treated as constitutionally protected")
	}
}

func TestProtectedDeletionRoles_MatchesThePerRoleCheck(t *testing.T) {
	// ProtectedDeletionRoles is documented as "the single source of truth for
	// the set" that persistence adapters use to express the prohibition in a
	// query. It must describe exactly the roles ProtectedFromDeletion accepts
	// — any divergence would mean the database prohibition and the in-process
	// check disagree about what is deletable.
	set := make(map[string]bool)
	for _, s := range ProtectedDeletionRoles() {
		set[s] = true
	}
	all := []Role{
		RoleConstitution, RoleGovernance, RoleEngSpec, RoleValidation,
		RoleEvidence, RoleAudit, RolePlanning, RoleReference, RoleWorking,
	}
	for _, r := range all {
		if set[string(r)] != r.ProtectedFromDeletion() {
			t.Errorf("ProtectedDeletionRoles() disagrees with ProtectedFromDeletion() for role %q", r)
		}
	}
}

func TestProtectedDeletionRoles_ReturnsAnIndependentCopy(t *testing.T) {
	// This is the single source of truth for a security-relevant set; a caller
	// mutating the returned slice must never corrupt what future callers see.
	got := ProtectedDeletionRoles()
	if len(got) == 0 {
		t.Fatal("expected a non-empty protected role set")
	}
	got[0] = "tampered"
	again := ProtectedDeletionRoles()
	if again[0] == "tampered" {
		t.Fatal("ProtectedDeletionRoles must return a fresh copy each call, not shared backing storage")
	}
}

// ---- §9.1 constitutional metadata keys -------------------------------------
//
// These keys are read by the §9.1 evaluators and determine computed Authority
// and the Replacement Test. A surface that permits arbitrary metadata writes
// must be able to exclude exactly this set — no more, no less.

func TestIsConstitutionalMetadataKey_ExactlyTheThreeInputKeys(t *testing.T) {
	cases := map[string]bool{
		"responsibilities": true,
		"citations":        true,
		"coverage":         true,
		"":                 false,
		"description":      false,
		"title":            false,
		"Coverage":         false, // case-sensitive: not an alias
		"coverage ":        false, // no trimming: not equivalent
	}
	for key, want := range cases {
		if got := IsConstitutionalMetadataKey(key); got != want {
			t.Errorf("IsConstitutionalMetadataKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestConstitutionalMetadataKeys_MatchesThePerKeyCheck(t *testing.T) {
	set := make(map[string]bool)
	for _, k := range ConstitutionalMetadataKeys() {
		set[k] = true
	}
	for _, k := range []string{"responsibilities", "citations", "coverage", "description", ""} {
		if set[k] != IsConstitutionalMetadataKey(k) {
			t.Errorf("ConstitutionalMetadataKeys() disagrees with IsConstitutionalMetadataKey() for %q", k)
		}
	}
}

func TestConstitutionalMetadataKeys_ReturnsAnIndependentCopy(t *testing.T) {
	got := ConstitutionalMetadataKeys()
	if len(got) == 0 {
		t.Fatal("expected a non-empty constitutional metadata key set")
	}
	got[0] = "tampered"
	again := ConstitutionalMetadataKeys()
	if again[0] == "tampered" {
		t.Fatal("ConstitutionalMetadataKeys must return a fresh copy each call, not shared backing storage")
	}
}

func TestNewID(t *testing.T) {
	a, b := NewID(), NewID()
	if a == "" || b == "" {
		t.Fatal("empty id")
	}
	if a == b {
		t.Fatal("ids should be unique")
	}
}
