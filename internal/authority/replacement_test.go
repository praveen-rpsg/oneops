package authority

import (
	"context"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// M4 WP-1 — the prospective four-part Replacement Test (§9.1).
//
// These assert the PRECEDENCE the resolver uses, not just pass/fail: when more
// than one conjunct fails the reported clause must be the first in §9.1 order,
// so the operation's rejection reason matches what the resolver would report
// once the supersedes edge exists.

func newTest(t *testing.T, f *gapGraph) *ReplacementTest {
	t.Helper()
	r := NewResolver(f)
	rt, err := NewReplacementTest(
		NewActiveDependencyEvaluator(r),
		NewResponsibilityEvaluator(f),
		NewArtifactCitationEvaluator(r, f),
		NewGapEvaluator(r, f),
	)
	if err != nil {
		t.Fatalf("NewReplacementTest: %v", err)
	}
	return rt
}

func evalT(t *testing.T, f *gapGraph) domain.ReplacementTestResult {
	t.Helper()
	res, err := newTest(t, f).Evaluate(context.Background(), "OLD", "NEW")
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return res
}

// clean is the baseline fixture where all four conjuncts hold.
func clean() *gapGraph {
	f := gapg().
		owns("OLD", "r1").owns("NEW", "r1").
		covers("OLD", "c1").covers("NEW", "c1")
	f.base("NEW")
	return f
}

func TestReplacementAllFourConjunctsPass(t *testing.T) {
	res := evalT(t, clean())
	if !res.Passed {
		t.Fatalf("expected pass, got clause %q evidence %v", res.FailedClause, res.Evidence)
	}
	if res.FailedClause != "" {
		t.Errorf("FailedClause = %q, want empty on pass", res.FailedClause)
	}
}

func TestReplacementRejectedByActiveDependency(t *testing.T) {
	f := clean()
	f.base("DEP")
	f.edge("DEP", "OLD", domain.EdgeKindDepends)
	res := evalT(t, f)
	if res.Passed || res.FailedClause != domain.ReasonSupersededActiveDependency {
		t.Fatalf("got passed=%v clause=%q, want failure on %q", res.Passed, res.FailedClause, domain.ReasonSupersededActiveDependency)
	}
	if len(res.Evidence) == 0 {
		t.Error("expected the active dependents as evidence")
	}
}

func TestReplacementRejectedByIncompleteResponsibilities(t *testing.T) {
	f := gapg().
		owns("OLD", "r1", "r2").owns("NEW", "r1").
		covers("OLD", "c1").covers("NEW", "c1")
	f.base("NEW")
	res := evalT(t, f)
	if res.Passed || res.FailedClause != domain.ReasonReplacementIncomplete {
		t.Fatalf("got passed=%v clause=%q, want %q", res.Passed, res.FailedClause, domain.ReasonReplacementIncomplete)
	}
}

func TestReplacementRejectedByActiveCitation(t *testing.T) {
	f := clean()
	f.base("CITER")
	f.cite("CITER", "OLD")
	res := evalT(t, f)
	if res.Passed || res.FailedClause != domain.ReasonActiveArtifactCitation {
		t.Fatalf("got passed=%v clause=%q, want %q", res.Passed, res.FailedClause, domain.ReasonActiveArtifactCitation)
	}
}

func TestReplacementRejectedByOperationalGap(t *testing.T) {
	f := gapg().
		owns("OLD", "r1").owns("NEW", "r1").
		covers("OLD", "c1", "c2").covers("NEW", "c1")
	f.base("NEW")
	res := evalT(t, f)
	if res.Passed || res.FailedClause != domain.ReasonOperationalGap {
		t.Fatalf("got passed=%v clause=%q, want %q", res.Passed, res.FailedClause, domain.ReasonOperationalGap)
	}
}

// Precedence: an active dependency outranks an incomplete replacement, matching
// the resolver's order. If this inverts, the operation and the resolver would
// disagree about the same pair.
func TestReplacementPrecedenceDependencyBeatsResponsibility(t *testing.T) {
	f := gapg().
		owns("OLD", "r1", "r2").owns("NEW", "r1").
		covers("OLD", "c1").covers("NEW", "c1")
	f.base("NEW")
	f.base("DEP")
	f.edge("DEP", "OLD", domain.EdgeKindDepends)
	res := evalT(t, f)
	if res.FailedClause != domain.ReasonSupersededActiveDependency {
		t.Fatalf("clause = %q, want dependency to outrank responsibility", res.FailedClause)
	}
}

// A missing evaluator must be a construction error, never a silently skipped
// conjunct — the resolver may treat them as optional, a constitutional
// precondition may not.
func TestReplacementRequiresAllFourEvaluators(t *testing.T) {
	r := NewResolver(gapg())
	if _, err := NewReplacementTest(NewActiveDependencyEvaluator(r), nil, nil, nil); err == nil {
		t.Fatal("expected an error when evaluators are missing")
	}
}
