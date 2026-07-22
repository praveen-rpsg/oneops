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

func TestNewID(t *testing.T) {
	a, b := NewID(), NewID()
	if a == "" || b == "" {
		t.Fatal("empty id")
	}
	if a == b {
		t.Fatal("ids should be unique")
	}
}
