package governance

import (
	"context"
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// M4 WP-1 step 2 — the §8 Replacement operation in the engine.

const (
	repBase = "ONEOPS-CFG-0100"
	repSucc = "ONEOPS-CFG-0200"
)

// stubTester is a ReplacementTester whose verdict the test controls.
type stubTester struct {
	res            domain.ReplacementTestResult
	err            error
	calls          int
	gotOld, gotNew string
}

func (s *stubTester) Evaluate(_ context.Context, oldCfgID, newCfgID string) (domain.ReplacementTestResult, error) {
	s.calls++
	s.gotOld, s.gotNew = oldCfgID, newCfgID
	return s.res, s.err
}

func replaceCmd() Command {
	return Command{
		Operation: domain.OpReplacement, CfgID: repBase, SuccessorID: repSucc,
		Actor: "architect@oneops", OperationID: "op-replace-1",
	}
}

func activeBase() *domain.ConfigObject {
	return obj(domain.LifecycleRatified, domain.RetentionCurrentBaseline, domain.AuthorityActive)
}

// The §8 outcome: Authority → Historical, Retention → Historical Record,
// Lifecycle unchanged (it is not named among §8's outputs).
func TestReplacementAppliesHistoricalOutcome(t *testing.T) {
	s := &mockStore{getObj: activeBase(), applyRV: 9}
	e := newEngine(t, s, AllowAllAuthorizer{})
	e.SetReplacementTester(&stubTester{res: domain.ReplacementTestResult{Passed: true}})

	res, err := e.Execute(context.Background(), replaceCmd())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.NewAuthority != domain.AuthorityHistorical {
		t.Errorf("Authority = %q, want %q", res.NewAuthority, domain.AuthorityHistorical)
	}
	if res.NewRetention != domain.RetentionHistoricalRecord {
		t.Errorf("Retention = %q, want %q", res.NewRetention, domain.RetentionHistoricalRecord)
	}
	if res.NewLifecycle != domain.LifecycleRatified {
		t.Errorf("Lifecycle = %q, want unchanged", res.NewLifecycle)
	}
	if s.lastEdge.Kind != domain.EdgeKindSupersedes || s.lastEdge.From != repSucc || s.lastEdge.To != repBase {
		t.Errorf("edge = %s -%s-> %s, want %s -supersedes-> %s",
			s.lastEdge.From, s.lastEdge.Kind, s.lastEdge.To, repSucc, repBase)
	}
}

// The test is consulted with (base, successor) in that order — reversing it
// would evaluate the wrong replacement.
func TestReplacementConsultsTesterWithBaseThenSuccessor(t *testing.T) {
	st := &stubTester{res: domain.ReplacementTestResult{Passed: true}}
	e := newEngine(t, &mockStore{getObj: activeBase(), applyRV: 2}, AllowAllAuthorizer{})
	e.SetReplacementTester(st)

	if _, err := e.Execute(context.Background(), replaceCmd()); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if st.calls != 1 || st.gotOld != repBase || st.gotNew != repSucc {
		t.Fatalf("tester called %d times with (%q,%q), want 1 with (%q,%q)",
			st.calls, st.gotOld, st.gotNew, repBase, repSucc)
	}
}

// A failed test rejects the operation, mutates nothing, and emits no audit event.
func TestReplacementRejectedWhenTestFails(t *testing.T) {
	spy := &spyAuditor{}
	s := &mockStore{getObj: activeBase(), applyRV: 2}
	e, err := NewEngine(s, AllowAllAuthorizer{}, spy)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	e.SetReplacementTester(&stubTester{res: domain.ReplacementTestResult{
		FailedClause: domain.ReasonSupersededActiveDependency,
		Evidence:     []string{"ONEOPS-CFG-0300"},
	}})

	var te *TransitionError
	if _, err := e.Execute(context.Background(), replaceCmd()); !errors.As(err, &te) {
		t.Fatalf("error = %v, want *TransitionError", err)
	}
	if s.applyCalls != 0 || s.edgeCalls != 0 {
		t.Errorf("mutated on a failed test: apply=%d edge=%d", s.applyCalls, s.edgeCalls)
	}
	if spy.calls != 0 {
		t.Errorf("audit appended %d events on a failed test, want 0", spy.calls)
	}
	if s.tx.committed {
		t.Error("committed despite a failed replacement test")
	}
}

// FAIL CLOSED: an engine with no tester must refuse Replacement, never perform
// it untested.
func TestReplacementRefusedWithoutTester(t *testing.T) {
	s := &mockStore{getObj: activeBase(), applyRV: 2}
	e := newEngine(t, s, AllowAllAuthorizer{})

	if _, err := e.Execute(context.Background(), replaceCmd()); !errors.Is(err, ErrReplacementTesterUnavailable) {
		t.Fatalf("error = %v, want ErrReplacementTesterUnavailable", err)
	}
	if s.applyCalls != 0 || s.edgeCalls != 0 {
		t.Error("mutated without a replacement test")
	}
}

// A tester error propagates and mutates nothing.
func TestReplacementTesterErrorPropagates(t *testing.T) {
	sentinel := errors.New("authority graph unavailable")
	s := &mockStore{getObj: activeBase(), applyRV: 2}
	e := newEngine(t, s, AllowAllAuthorizer{})
	e.SetReplacementTester(&stubTester{err: sentinel})

	if _, err := e.Execute(context.Background(), replaceCmd()); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want %v", err, sentinel)
	}
	if s.applyCalls != 0 {
		t.Error("mutated despite a tester error")
	}
}

// Structural preconditions are still enforced purely, before any I/O.
func TestReplacementStructuralPreconditions(t *testing.T) {
	base := activeBase()
	for _, tc := range []struct {
		name string
		cmd  Command
	}{
		{"successor required", Command{Operation: domain.OpReplacement, CfgID: repBase}},
		{"self-supersession rejected", Command{Operation: domain.OpReplacement, CfgID: repBase, SuccessorID: repBase}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var te *TransitionError
			if _, err := planTransition(domain.OpReplacement, base, tc.cmd); !errors.As(err, &te) {
				t.Fatalf("expected *TransitionError, got %v", err)
			}
		})
	}
}

// §9.3-5 as interpreted by CI-1 Issue 2 changes an Execute-level outcome that no
// test previously covered: planTransition is pure and never validates, so the
// existing TestArchiving asserts only the PLAN. Execute validates the resulting
// object (engine.go), so archiving an Active artifact into one of the three
// classes §9.3-5 names is now refused — and archiving it to audit_record, which
// the Council placed outside the invariant, still succeeds.
func TestArchivingActiveRespectsInvariant5(t *testing.T) {
	active := func() *domain.ConfigObject {
		return obj(domain.LifecycleComplete, domain.RetentionWorkingMaterial, domain.AuthorityActive)
	}
	archive := func(rc domain.RetentionClass) error {
		s := &mockStore{getObj: active(), applyRV: 2}
		e := newEngine(t, s, AllowAllAuthorizer{})
		_, err := e.Execute(context.Background(), Command{
			Operation: domain.OpArchiving, CfgID: "c1", Actor: "archivist",
			OperationID: "op-arch", TargetRetention: rc,
		})
		return err
	}

	for _, rc := range []domain.RetentionClass{
		domain.RetentionHistoricalRecord, domain.RetentionHistoricalEvidence, domain.RetentionSupersededPlan,
	} {
		if err := archive(rc); err == nil {
			t.Errorf("archiving an Active artifact to %s must be refused (§9.3-5)", rc)
		}
	}

	if err := archive(domain.RetentionAuditRecord); err != nil {
		t.Errorf("archiving an Active artifact to audit_record must succeed (CI-1 Issue 2): %v", err)
	}
}
