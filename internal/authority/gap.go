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

// GapEvaluator answers the graph-independent Replacement-Test clause
// noGapIfRemoved(): would removing a superseded configuration leave any of its
// declared operational coverage unowned by an ACTIVE configuration? Coverage ids
// are opaque logical identifiers read only from Configuration Object metadata
// (never inferred from names, scanned from source, read from documents, or probed
// from runtime systems); comparison is deterministic. "Active" reuses the M3.1
// resolver's active set exactly as the M3.2/M3.4 clauses do — a configuration
// reachable from the in-force baseline. It implements no governance, approval,
// replacement execution, graph mutation, persistence, or cache; those remain
// deferred. This is the fourth and final technical predicate.
type GapEvaluator struct {
	resolver *Resolver
	data     domain.CoverageGraph
}

// NewGapEvaluator builds an evaluator over an existing resolver (for the active
// set and object existence) and a metadata-backed coverage source.
func NewGapEvaluator(resolver *Resolver, data domain.CoverageGraph) *GapEvaluator {
	return &GapEvaluator{resolver: resolver, data: data}
}

// coverageOf returns the validated, sorted coverage ids declared by cfgID.
// Whitespace-only or empty segments are malformed metadata; duplicate ids are
// rejected. Coverage ids are opaque logical identifiers, not object references,
// so no existence check is performed. Each failure is a structured
// *ValidationError — never a silent failure.
func (e *GapEvaluator) coverageOf(ctx context.Context, cfgID string) ([]string, error) {
	raw, err := e.data.Coverage(ctx, cfgID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		id := strings.TrimSpace(c)
		if id == "" {
			return nil, &ValidationError{Kind: "malformed_coverage_metadata", CfgID: cfgID, Detail: "coverage metadata contains an empty id"}
		}
		if seen[id] {
			return nil, &ValidationError{Kind: "duplicate_coverage", CfgID: cfgID, Detail: "coverage " + id + " declared more than once"}
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// gapAfterRemoval returns the coverage ids declared by cfgID that no other ACTIVE
// configuration provides — the coverage that would be left unowned if cfgID were
// removed. It is a pure function over the coverage source and a precomputed active
// set: it unions the coverage of every active configuration except cfgID itself,
// then subtracts that union from cfgID's own coverage. An object that declares no
// coverage can never create a gap. Every scanned active configuration's coverage
// is validated, so malformed or duplicate coverage metadata surfaces as a
// *ValidationError rather than a silent wrong answer.
func (e *GapEvaluator) gapAfterRemoval(ctx context.Context, cfgID string, active map[string]reachInfo) ([]string, error) {
	own, err := e.coverageOf(ctx, cfgID)
	if err != nil {
		return nil, err
	}
	if len(own) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(active))
	for id := range active {
		ids = append(ids, id)
	}
	sort.Strings(ids) // deterministic scan order
	covered := map[string]bool{}
	for _, a := range ids {
		if a == cfgID {
			continue
		}
		cov, err := e.coverageOf(ctx, a)
		if err != nil {
			return nil, err
		}
		for _, c := range cov {
			covered[c] = true
		}
	}
	var uncovered []string
	for _, c := range own { // own is already sorted → uncovered stays sorted
		if !covered[c] {
			uncovered = append(uncovered, c)
		}
	}
	return uncovered, nil
}

// EvaluateGap reports whether removing cfgID would leave any of its declared
// operational coverage unowned by an ACTIVE configuration, with the uncovered
// coverage ids as evidence. A missing object yields a *ValidationError.
func (e *GapEvaluator) EvaluateGap(ctx context.Context, cfgID string) (domain.GapResult, error) {
	ctx, span := tracer.Start(ctx, "GapEvaluator.EvaluateGap",
		trace.WithAttributes(attribute.String("authority.cfg_id", cfgID)))
	defer span.End()

	exists, err := e.resolver.data.ObjectExists(ctx, cfgID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "exists")
		return domain.GapResult{}, err
	}
	if !exists {
		return domain.GapResult{}, &ValidationError{Kind: "missing_object", CfgID: cfgID, Detail: "object does not exist"}
	}
	active, err := e.resolver.computeActiveSet(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "active set")
		return domain.GapResult{}, err
	}
	span.SetAttributes(attribute.Int("authority.active_set_size", len(active)))
	uncovered, err := e.gapAfterRemoval(ctx, cfgID, active)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "evaluate")
		return domain.GapResult{}, err
	}
	span.SetAttributes(
		attribute.Bool("authority.has_gap", len(uncovered) > 0),
		attribute.Int("authority.uncovered_count", len(uncovered)),
	)
	return domain.GapResult{
		CfgID:                 cfgID,
		HasGap:                len(uncovered) > 0,
		UncoveredCapabilities: uncovered,
	}, nil
}

// EvaluateReplacement reports whether newCfgID provides every operational
// coverage id of oldCfgID (the targeted form of noGapIfRemoved). An empty target
// or a non-existent old/new object yields a *ValidationError (broken replacement
// mapping / empty replacement target). It performs no approval or execution.
func (e *GapEvaluator) EvaluateReplacement(ctx context.Context, oldCfgID, newCfgID string) (domain.GapReplacementResult, error) {
	ctx, span := tracer.Start(ctx, "GapEvaluator.EvaluateReplacement",
		trace.WithAttributes(attribute.String("authority.old", oldCfgID), attribute.String("authority.new", newCfgID)))
	defer span.End()

	if strings.TrimSpace(newCfgID) == "" {
		return domain.GapReplacementResult{}, &ValidationError{Kind: "empty_replacement_target", CfgID: oldCfgID, Detail: "replacement target is empty"}
	}
	for _, id := range []string{oldCfgID, newCfgID} {
		exists, err := e.resolver.data.ObjectExists(ctx, id)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "exists")
			return domain.GapReplacementResult{}, err
		}
		if !exists {
			return domain.GapReplacementResult{}, &ValidationError{Kind: "broken_replacement", CfgID: id, Detail: "object does not exist"}
		}
	}
	oldCov, err := e.coverageOf(ctx, oldCfgID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "coverage")
		return domain.GapReplacementResult{}, err
	}
	newCov, err := e.coverageOf(ctx, newCfgID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "coverage")
		return domain.GapReplacementResult{}, err
	}
	newSet := map[string]bool{}
	for _, c := range newCov {
		newSet[c] = true
	}
	var covered, missing []string
	for _, c := range oldCov {
		if newSet[c] {
			covered = append(covered, c)
		} else {
			missing = append(missing, c)
		}
	}
	sort.Strings(covered)
	sort.Strings(missing)
	span.SetAttributes(attribute.Bool("authority.replacement_complete", len(missing) == 0))
	return domain.GapReplacementResult{
		OldCfgID: oldCfgID,
		NewCfgID: newCfgID,
		Complete: len(missing) == 0,
		Covered:  covered,
		Missing:  missing,
	}, nil
}

// EvaluateBatch evaluates many objects, sharing one active-set computation.
// Results are returned in input order (deterministic).
func (e *GapEvaluator) EvaluateBatch(ctx context.Context, cfgIDs []string) ([]domain.GapResult, error) {
	ctx, span := tracer.Start(ctx, "GapEvaluator.EvaluateBatch",
		trace.WithAttributes(attribute.Int("authority.batch_size", len(cfgIDs))))
	defer span.End()

	active, err := e.resolver.computeActiveSet(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "active set")
		return nil, err
	}
	out := make([]domain.GapResult, 0, len(cfgIDs))
	for _, id := range cfgIDs {
		exists, err := e.resolver.data.ObjectExists(ctx, id)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "exists")
			return nil, err
		}
		if !exists {
			return nil, &ValidationError{Kind: "missing_object", CfgID: id, Detail: "object does not exist"}
		}
		uncovered, err := e.gapAfterRemoval(ctx, id, active)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "evaluate")
			return nil, err
		}
		out = append(out, domain.GapResult{
			CfgID:                 id,
			HasGap:                len(uncovered) > 0,
			UncoveredCapabilities: uncovered,
		})
	}
	span.SetAttributes(attribute.Int("authority.resolved_count", len(out)))
	return out, nil
}
