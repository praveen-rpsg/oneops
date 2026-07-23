package authority

import (
	"context"
	"sort"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/rpsg/oneops/internal/domain"
)

// ResponsibilityEvaluator answers the graph-independent Replacement-Test clause
// owns(allResponsibilities): has every responsibility of a superseded object
// been transferred to its replacement(s)? Ownership is read only from
// Configuration Object metadata; comparison is deterministic. It implements no
// governance, citations, gap analysis, persistence, or cache.
type ResponsibilityEvaluator struct {
	data domain.ResponsibilityGraph
}

// NewResponsibilityEvaluator builds an evaluator over a responsibility source.
func NewResponsibilityEvaluator(data domain.ResponsibilityGraph) *ResponsibilityEvaluator {
	return &ResponsibilityEvaluator{data: data}
}

// responsibilitiesOf returns cfgID's owned responsibility ids, sorted and
// validated. Duplicate ids are rejected with a *ValidationError.
func (e *ResponsibilityEvaluator) responsibilitiesOf(ctx context.Context, cfgID string) ([]string, error) {
	raw, err := e.data.Responsibilities(ctx, cfgID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if seen[r] {
			return nil, &ValidationError{Kind: "duplicate_responsibility", CfgID: cfgID, Detail: "responsibility " + r + " declared more than once"}
		}
		seen[r] = true
		out = append(out, r)
	}
	sort.Strings(out)
	return out, nil
}

// EvaluateResponsibilities returns the responsibilities owned by cfgID.
func (e *ResponsibilityEvaluator) EvaluateResponsibilities(ctx context.Context, cfgID string) (domain.ResponsibilityResult, error) {
	ctx, span := tracer.Start(ctx, "ResponsibilityEvaluator.EvaluateResponsibilities",
		trace.WithAttributes(attribute.String("authority.cfg_id", cfgID)))
	defer span.End()

	exists, err := e.data.ObjectExists(ctx, cfgID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "exists")
		return domain.ResponsibilityResult{}, err
	}
	if !exists {
		return domain.ResponsibilityResult{}, &ValidationError{Kind: "missing_object", CfgID: cfgID, Detail: "object does not exist"}
	}
	resp, err := e.responsibilitiesOf(ctx, cfgID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "responsibilities")
		return domain.ResponsibilityResult{}, err
	}
	span.SetAttributes(attribute.Int("authority.responsibility_count", len(resp)))
	return domain.ResponsibilityResult{CfgID: cfgID, Responsibilities: resp}, nil
}

// EvaluateReplacement reports whether newCfgID owns every responsibility of
// oldCfgID. An empty target or a non-existent object yields a *ValidationError.
func (e *ResponsibilityEvaluator) EvaluateReplacement(ctx context.Context, oldCfgID, newCfgID string) (domain.ReplacementResult, error) {
	ctx, span := tracer.Start(ctx, "ResponsibilityEvaluator.EvaluateReplacement",
		trace.WithAttributes(attribute.String("authority.old", oldCfgID), attribute.String("authority.new", newCfgID)))
	defer span.End()

	if strings.TrimSpace(newCfgID) == "" {
		return domain.ReplacementResult{}, &ValidationError{Kind: "empty_replacement_target", CfgID: oldCfgID, Detail: "replacement target is empty"}
	}
	for _, id := range []string{oldCfgID, newCfgID} {
		exists, err := e.data.ObjectExists(ctx, id)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "exists")
			return domain.ReplacementResult{}, err
		}
		if !exists {
			return domain.ReplacementResult{}, &ValidationError{Kind: "broken_replacement", CfgID: id, Detail: "object does not exist"}
		}
	}

	oldResp, err := e.responsibilitiesOf(ctx, oldCfgID)
	if err != nil {
		return domain.ReplacementResult{}, err
	}
	newResp, err := e.responsibilitiesOf(ctx, newCfgID)
	if err != nil {
		return domain.ReplacementResult{}, err
	}
	newSet := map[string]bool{}
	for _, r := range newResp {
		newSet[r] = true
	}
	var transferred, missing []string
	for _, r := range oldResp {
		if newSet[r] {
			transferred = append(transferred, r)
		} else {
			missing = append(missing, r)
		}
	}
	sort.Strings(transferred)
	sort.Strings(missing)
	span.SetAttributes(attribute.Bool("authority.replacement_complete", len(missing) == 0))
	return domain.ReplacementResult{
		OldCfgID: oldCfgID, NewCfgID: newCfgID,
		Complete: len(missing) == 0, Transferred: transferred, Missing: missing,
	}, nil
}

// EvaluateBatch returns the responsibilities of each object, in input order.
func (e *ResponsibilityEvaluator) EvaluateBatch(ctx context.Context, cfgIDs []string) ([]domain.ResponsibilityResult, error) {
	ctx, span := tracer.Start(ctx, "ResponsibilityEvaluator.EvaluateBatch",
		trace.WithAttributes(attribute.Int("authority.batch_size", len(cfgIDs))))
	defer span.End()

	out := make([]domain.ResponsibilityResult, 0, len(cfgIDs))
	for _, id := range cfgIDs {
		res, err := e.EvaluateResponsibilities(ctx, id)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "batch")
			return nil, err
		}
		out = append(out, res)
	}
	return out, nil
}

// replacementComplete reports whether the union of the superseders' owned
// responsibilities covers every responsibility of oldCfgID, returning the
// missing ids. An empty old responsibility set is trivially complete.
func (e *ResponsibilityEvaluator) replacementComplete(ctx context.Context, oldCfgID string, superseders []string) (bool, []string, error) {
	oldResp, err := e.responsibilitiesOf(ctx, oldCfgID)
	if err != nil {
		return false, nil, err
	}
	if len(oldResp) == 0 {
		return true, nil, nil
	}
	covered := map[string]bool{}
	for _, s := range superseders {
		sResp, err := e.responsibilitiesOf(ctx, s)
		if err != nil {
			return false, nil, err
		}
		for _, r := range sResp {
			covered[r] = true
		}
	}
	var missing []string
	for _, r := range oldResp {
		if !covered[r] {
			missing = append(missing, r)
		}
	}
	sort.Strings(missing)
	return len(missing) == 0, missing, nil
}
