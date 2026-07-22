package authority

import (
	"context"
	"sort"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/rpsg/oneops/internal/domain"
)

// activeDependentsOf returns the Active configurations that directly depend on
// cfgID (incoming depends/extends edges whose source is in the active set),
// sorted and deduplicated. Supersedes edges are never dependencies. It is a pure
// function over the graph port and a precomputed active set — no traversal
// modification, no resolver state.
func activeDependentsOf(ctx context.Context, data domain.AuthorityGraph, cfgID string, active map[string]reachInfo) ([]string, error) {
	in, err := data.EdgesTo(ctx, cfgID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var deps []string
	for _, e := range in {
		if e.EdgeKind != domain.EdgeKindDepends && e.EdgeKind != domain.EdgeKindExtends {
			continue
		}
		if _, isActive := active[e.FromCfg]; isActive && !seen[e.FromCfg] {
			seen[e.FromCfg] = true
			deps = append(deps, e.FromCfg)
		}
	}
	sort.Strings(deps)
	return deps, nil
}

// ActiveDependencyEvaluator answers the graph-computable Replacement-Test clause:
// is a configuration still required by an Active configuration? It reuses the
// M3.1 AuthorityResolver's active-set computation and the M2 graph accessors; it
// does not implement responsibilities, citations, gap analysis, or governance —
// those remain deferred to later milestones.
type ActiveDependencyEvaluator struct {
	resolver *Resolver
}

// NewActiveDependencyEvaluator builds an evaluator over an existing resolver.
func NewActiveDependencyEvaluator(resolver *Resolver) *ActiveDependencyEvaluator {
	return &ActiveDependencyEvaluator{resolver: resolver}
}

// EvaluateActiveDependencies reports whether cfgID is still required by an Active
// configuration, with the list of active dependents as evidence. A missing
// object yields a *ValidationError.
func (e *ActiveDependencyEvaluator) EvaluateActiveDependencies(ctx context.Context, cfgID string) (domain.ActiveDependencyResult, error) {
	ctx, span := tracer.Start(ctx, "ActiveDependencyEvaluator.EvaluateActiveDependencies",
		trace.WithAttributes(attribute.String("authority.cfg_id", cfgID)))
	defer span.End()

	exists, err := e.resolver.data.ObjectExists(ctx, cfgID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "exists")
		return domain.ActiveDependencyResult{}, err
	}
	if !exists {
		return domain.ActiveDependencyResult{}, &ValidationError{Kind: "missing_object", CfgID: cfgID, Detail: "object does not exist"}
	}
	active, err := e.resolver.computeActiveSet(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "active set")
		return domain.ActiveDependencyResult{}, err
	}
	res, err := e.evaluate(ctx, cfgID, active)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "evaluate")
		return domain.ActiveDependencyResult{}, err
	}
	span.SetAttributes(attribute.Bool("authority.has_active_dependency", res.HasActiveDependency))
	return res, nil
}

// EvaluateBatch evaluates many objects, sharing one active-set computation.
// Results are returned in input order (deterministic).
func (e *ActiveDependencyEvaluator) EvaluateBatch(ctx context.Context, cfgIDs []string) ([]domain.ActiveDependencyResult, error) {
	ctx, span := tracer.Start(ctx, "ActiveDependencyEvaluator.EvaluateBatch",
		trace.WithAttributes(attribute.Int("authority.batch_size", len(cfgIDs))))
	defer span.End()

	active, err := e.resolver.computeActiveSet(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "active set")
		return nil, err
	}
	out := make([]domain.ActiveDependencyResult, 0, len(cfgIDs))
	for _, id := range cfgIDs {
		exists, err := e.resolver.data.ObjectExists(ctx, id)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, &ValidationError{Kind: "missing_object", CfgID: id, Detail: "object does not exist"}
		}
		res, err := e.evaluate(ctx, id, active)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	span.SetAttributes(attribute.Int("authority.resolved_count", len(out)))
	return out, nil
}

func (e *ActiveDependencyEvaluator) evaluate(ctx context.Context, cfgID string, active map[string]reachInfo) (domain.ActiveDependencyResult, error) {
	deps, err := activeDependentsOf(ctx, e.resolver.data, cfgID, active)
	if err != nil {
		return domain.ActiveDependencyResult{}, err
	}
	return domain.ActiveDependencyResult{
		CfgID:               cfgID,
		HasActiveDependency: len(deps) > 0,
		ActiveDependents:    deps,
	}, nil
}
