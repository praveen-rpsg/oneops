package governance

import (
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

func obj(lc domain.Lifecycle, rc domain.RetentionClass, au domain.Authority) *domain.ConfigObject {
	return &domain.ConfigObject{
		CfgID: "c1", Artifact: "a.md", Version: "1.0.0", Role: domain.RoleReference,
		Lifecycle: lc, RetentionClass: rc, Authority: au, RetentionPolicy: "permanent", RowVersion: 1,
	}
}

func mustPlan(t *testing.T, op domain.ConfigurationOperation, o *domain.ConfigObject, cmd Command) plan {
	t.Helper()
	p, err := planTransition(op, o, cmd)
	if err != nil {
		t.Fatalf("%s: unexpected error: %v", op, err)
	}
	return p
}

func expectInvalid(t *testing.T, op domain.ConfigurationOperation, o *domain.ConfigObject, cmd Command) {
	t.Helper()
	_, err := planTransition(op, o, cmd)
	var te *TransitionError
	if !errors.As(err, &te) {
		t.Fatalf("%s: expected TransitionError, got %v", op, err)
	}
}

func TestRatification(t *testing.T) {
	p := mustPlan(t, domain.OpRatification, obj(domain.LifecycleInReview, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative), Command{})
	if p.Lifecycle != domain.LifecycleRatified || p.Retention != domain.RetentionCurrentBaseline || p.Authority != domain.AuthorityActive {
		t.Fatalf("ratify plan = %+v", p)
	}
	expectInvalid(t, domain.OpRatification, obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, domain.AuthorityActive), Command{})
}

func TestApproval(t *testing.T) {
	p := mustPlan(t, domain.OpApproval, obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative), Command{})
	if p.Lifecycle != domain.LifecycleApproved || p.Authority != domain.AuthorityActive || p.Retention != domain.RetentionWorkingMaterial {
		t.Fatalf("approve plan = %+v", p)
	}
	expectInvalid(t, domain.OpApproval, obj(domain.LifecycleComplete, domain.RetentionWorkingMaterial, domain.AuthorityActive), Command{})
}

func TestSuspension(t *testing.T) {
	p := mustPlan(t, domain.OpSuspension, obj(domain.LifecycleInProgress, domain.RetentionWorkingMaterial, domain.AuthorityActive), Command{})
	if p.Lifecycle != domain.LifecycleSuspended || p.Authority != domain.AuthorityActive {
		t.Fatalf("suspend plan = %+v", p)
	}
	expectInvalid(t, domain.OpSuspension, obj(domain.LifecycleWithdrawn, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative), Command{})
	expectInvalid(t, domain.OpSuspension, obj(domain.LifecycleSuspended, domain.RetentionWorkingMaterial, domain.AuthorityActive), Command{})
}

func TestDeprecation(t *testing.T) {
	p := mustPlan(t, domain.OpDeprecation, obj(domain.LifecycleApproved, domain.RetentionCurrentPlanning, domain.AuthorityActive), Command{})
	if p.Lifecycle != domain.LifecycleDeprecated || p.Authority != domain.AuthorityActive {
		t.Fatalf("deprecate plan = %+v", p)
	}
	expectInvalid(t, domain.OpDeprecation, obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative), Command{})
}

func TestWithdrawal(t *testing.T) {
	// once governed (active) → historical
	p := mustPlan(t, domain.OpWithdrawal, obj(domain.LifecycleApproved, domain.RetentionCurrentPlanning, domain.AuthorityActive), Command{})
	if p.Lifecycle != domain.LifecycleWithdrawn || p.Authority != domain.AuthorityHistorical {
		t.Fatalf("withdraw(active) plan = %+v", p)
	}
	// never governed → non-normative
	p2 := mustPlan(t, domain.OpWithdrawal, obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative), Command{})
	if p2.Authority != domain.AuthorityNonNormative {
		t.Fatalf("withdraw(draft) plan = %+v", p2)
	}
	expectInvalid(t, domain.OpWithdrawal, obj(domain.LifecycleWithdrawn, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative), Command{})
}

func TestArchiving(t *testing.T) {
	p := mustPlan(t, domain.OpArchiving, obj(domain.LifecycleComplete, domain.RetentionWorkingMaterial, domain.AuthorityActive),
		Command{TargetRetention: domain.RetentionHistoricalRecord})
	if p.Retention != domain.RetentionHistoricalRecord || p.Authority != domain.AuthorityActive || p.Lifecycle != domain.LifecycleComplete {
		t.Fatalf("archive plan = %+v (authority must be unchanged)", p)
	}
	// non-archival target rejected
	expectInvalid(t, domain.OpArchiving, obj(domain.LifecycleComplete, domain.RetentionWorkingMaterial, domain.AuthorityActive),
		Command{TargetRetention: domain.RetentionCurrentBaseline})
}

func TestDeletion(t *testing.T) {
	p := mustPlan(t, domain.OpDeletion, obj(domain.LifecycleDraft, domain.RetentionWorkingMaterial, domain.AuthorityNonNormative), Command{})
	if !p.Remove {
		t.Fatalf("delete plan should remove: %+v", p)
	}
	expectInvalid(t, domain.OpDeletion, obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, domain.AuthorityActive), Command{})
}

func TestUnsupportedOperations(t *testing.T) {
	// Extension (WP-2) and Replacement (WP-1) are implemented and deliberately absent.
	for _, op := range []domain.ConfigurationOperation{
		domain.OpAmendment,
		domain.OpBaselineFreeze, domain.OpHistoricalPreservation,
	} {
		if _, err := planTransition(op, obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, domain.AuthorityActive), Command{}); !errors.Is(err, ErrUnsupportedOperation) {
			t.Errorf("%s: expected ErrUnsupportedOperation, got %v", op, err)
		}
	}
}
