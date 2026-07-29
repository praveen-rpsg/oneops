package authority

import (
	"context"
	"errors"

	"github.com/rpsg/oneops/internal/domain"
)

// ReplacementTest evaluates the four-part Replacement Test (Configuration State
// Model §9.1) PROSPECTIVELY, for a (base, successor) pair that is not yet
// related by a supersedes edge.
//
// The resolver already applies the same four conjuncts, but only to state that
// EXISTS: resolveOne reaches them via the "superseded" branch. §8 Replacement is
// the operation that creates the supersedes edge, so at command time that branch
// is unreachable and the resolver would report the base as simply Active. This
// type answers the prospective question instead, by composing the same four
// evaluators in the same precedence order — it re-implements none of them, so
// the operation and the resolver can never disagree about a pair.
//
// All four evaluators are REQUIRED. The resolver treats them as optional and
// skips a clause when its data source is absent, which is correct for a
// best-effort read model. For a constitutional precondition a silently skipped
// conjunct is a correctness hole, so absence is a construction error here.
type ReplacementTest struct {
	deps  *ActiveDependencyEvaluator
	resp  *ResponsibilityEvaluator
	cites *ArtifactCitationEvaluator
	gaps  *GapEvaluator
}

// NewReplacementTest composes the four evaluators. All are required.
func NewReplacementTest(
	deps *ActiveDependencyEvaluator,
	resp *ResponsibilityEvaluator,
	cites *ArtifactCitationEvaluator,
	gaps *GapEvaluator,
) (*ReplacementTest, error) {
	if deps == nil || resp == nil || cites == nil || gaps == nil {
		return nil, errors.New("authority: ReplacementTest requires all four evaluators")
	}
	return &ReplacementTest{deps: deps, resp: resp, cites: cites, gaps: gaps}, nil
}

// Evaluate reports whether replacing oldCfgID with newCfgID satisfies all four
// §9.1 conjuncts. It short-circuits on the first failure, preserving the
// resolver's precedence order so the reported clause matches what the resolver
// would report once the edge exists.
func (t *ReplacementTest) Evaluate(ctx context.Context, oldCfgID, newCfgID string) (domain.ReplacementTestResult, error) {
	res := domain.ReplacementTestResult{OldCfgID: oldCfgID, NewCfgID: newCfgID}

	// §9.1 — noActiveDependencyOn(old)
	dep, err := t.deps.EvaluateActiveDependencies(ctx, oldCfgID)
	if err != nil {
		return domain.ReplacementTestResult{}, err
	}
	if dep.HasActiveDependency {
		res.FailedClause, res.Evidence = domain.ReasonSupersededActiveDependency, dep.ActiveDependents
		return res, nil
	}

	// §9.1 — owns(new, allResponsibilities(old))
	rr, err := t.resp.EvaluateReplacement(ctx, oldCfgID, newCfgID)
	if err != nil {
		return domain.ReplacementTestResult{}, err
	}
	if !rr.Complete {
		res.FailedClause, res.Evidence = domain.ReasonReplacementIncomplete, rr.Missing
		return res, nil
	}

	// §9.1 — noActiveArtifactCites(old)
	cr, err := t.cites.EvaluateReplacement(ctx, oldCfgID, newCfgID)
	if err != nil {
		return domain.ReplacementTestResult{}, err
	}
	if !cr.Cleared {
		res.FailedClause, res.Evidence = domain.ReasonActiveArtifactCitation, cr.Remaining
		return res, nil
	}

	// §9.1 — noGapIfRemoved(old)
	gr, err := t.gaps.EvaluateReplacement(ctx, oldCfgID, newCfgID)
	if err != nil {
		return domain.ReplacementTestResult{}, err
	}
	if !gr.Complete {
		res.FailedClause, res.Evidence = domain.ReasonOperationalGap, gr.Missing
		return res, nil
	}

	res.Passed = true
	return res, nil
}
