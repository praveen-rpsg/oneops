// Package authority computes the constitutional authority of Configuration
// Objects from the M2 dependency graph, supersession, and baseline membership
// (Configuration State Model §2, §9). It consumes the graph; it never modifies
// traversal logic, and the graph never understands authority.
package authority

import (
	"context"
	"fmt"
	"sort"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/rpsg/oneops/internal/domain"
)

var tracer = otel.Tracer("github.com/rpsg/oneops/internal/authority")

// ValidationError is a structured graph-inconsistency error surfaced by the
// resolver (broken supersession chain, cycle affecting authority, etc.).
type ValidationError struct {
	Kind   string // machine-readable category
	CfgID  string // the object at which the inconsistency was found
	Detail string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("authority validation failed [%s] at %q: %s", e.Kind, e.CfgID, e.Detail)
}

// Resolver computes authority. It holds no state and performs no caching.
type Resolver struct {
	data domain.AuthorityGraph
}

// NewResolver builds a resolver over the given data port.
func NewResolver(data domain.AuthorityGraph) *Resolver {
	return &Resolver{data: data}
}

// reachInfo records how a node entered the Active set.
type reachInfo struct {
	baseline bool
	via      string
}

// ResolveAuthority computes the authority of a single object.
func (r *Resolver) ResolveAuthority(ctx context.Context, cfgID string) (domain.AuthorityResult, error) {
	ctx, span := tracer.Start(ctx, "AuthorityResolver.ResolveAuthority",
		trace.WithAttributes(attribute.String("authority.cfg_id", cfgID)))
	defer span.End()

	active, err := r.computeActiveSet(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "active set")
		return domain.AuthorityResult{}, err
	}
	res, err := r.resolveOne(ctx, cfgID, active)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "resolve")
		return domain.AuthorityResult{}, err
	}
	span.SetAttributes(attribute.String("authority.state", string(res.State)))
	return res, nil
}

// ResolveBatch computes authority for many objects, sharing one baseline
// computation. Results are returned in the input order (deterministic).
func (r *Resolver) ResolveBatch(ctx context.Context, cfgIDs []string) ([]domain.AuthorityResult, error) {
	ctx, span := tracer.Start(ctx, "AuthorityResolver.ResolveBatch",
		trace.WithAttributes(attribute.Int("authority.batch_size", len(cfgIDs))))
	defer span.End()

	active, err := r.computeActiveSet(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "active set")
		return nil, err
	}
	out := make([]domain.AuthorityResult, 0, len(cfgIDs))
	for _, id := range cfgIDs {
		res, err := r.resolveOne(ctx, id, active)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "resolve")
			return nil, err
		}
		out = append(out, res)
	}
	span.SetAttributes(attribute.Int("authority.resolved_count", len(out)))
	return out, nil
}

// ResolveGraph computes authority for root and its transitive dependencies. It
// first validates the scope for supersession cycles (broken chains), returning a
// *ValidationError if the graph is inconsistent. Results are ordered by cfg_id.
func (r *Resolver) ResolveGraph(ctx context.Context, root string) ([]domain.AuthorityResult, error) {
	ctx, span := tracer.Start(ctx, "AuthorityResolver.ResolveGraph",
		trace.WithAttributes(attribute.String("authority.root", root)))
	defer span.End()

	exists, err := r.data.ObjectExists(ctx, root)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "exists")
		return nil, err
	}
	if !exists {
		return nil, &ValidationError{Kind: "missing_object", CfgID: root, Detail: "root does not exist"}
	}

	deps, err := r.data.RecursiveDependencies(ctx, root)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "traverse")
		return nil, err
	}
	scope := make([]string, 0, len(deps)+1)
	scope = append(scope, root)
	for _, n := range deps {
		scope = append(scope, n.CfgID)
	}
	sort.Strings(scope)

	if err := r.validateSupersession(ctx, scope); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "validation")
		return nil, err
	}

	active, err := r.computeActiveSet(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "active set")
		return nil, err
	}
	out := make([]domain.AuthorityResult, 0, len(scope))
	for _, id := range scope {
		res, err := r.resolveOne(ctx, id, active)
		if err != nil {
			return nil, err
		}
		out = append(out, res)
	}
	span.SetAttributes(attribute.Int("authority.resolved_count", len(out)))
	return out, nil
}

// resolveOne applies the State Model rules in order: superseded → Historical;
// baseline-reachable → Active; exists-but-unreachable → Non-Normative;
// otherwise → Unknown.
func (r *Resolver) resolveOne(ctx context.Context, cfgID string, active map[string]reachInfo) (domain.AuthorityResult, error) {
	exists, err := r.data.ObjectExists(ctx, cfgID)
	if err != nil {
		return domain.AuthorityResult{}, err
	}
	if !exists {
		return domain.AuthorityResult{
			CfgID: cfgID, State: domain.AuthorityStateUnknown, Reason: domain.ReasonObjectNotFound,
			Evidence: domain.AuthorityEvidence{Notes: "no configuration object with this id"},
		}, nil
	}

	superseders, err := r.supersededBy(ctx, cfgID)
	if err != nil {
		return domain.AuthorityResult{}, err
	}
	info, isActive := active[cfgID]

	// Rule 1 — replacement, gated by the graph-computable Replacement-Test clause.
	if len(superseders) > 0 {
		// A current-baseline member that is also superseded is contradictory
		// (§9.3 invariant 1: current_baseline ⇒ Active) → indeterminate.
		if isActive && info.baseline {
			return domain.AuthorityResult{
				CfgID: cfgID, State: domain.AuthorityStateUnknown,
				Reason:   domain.ReasonInconsistentBaselineSuperseded,
				Evidence: domain.AuthorityEvidence{BaselineMember: true, SupersededBy: superseders, Notes: "baseline member is also superseded"},
			}, nil
		}
		// M3.2 — noActiveDependencyOn: a superseded config still required by an
		// Active configuration remains Active (Replacement Test, §9.1). Only when
		// no Active configuration depends on it may it resolve Historical.
		activeDeps, err := activeDependentsOf(ctx, r.data, cfgID, active)
		if err != nil {
			return domain.AuthorityResult{}, err
		}
		if len(activeDeps) > 0 {
			return domain.AuthorityResult{
				CfgID: cfgID, State: domain.AuthorityStateActive, Reason: domain.ReasonSupersededActiveDependency,
				Evidence: domain.AuthorityEvidence{SupersededBy: superseders, ActiveDependents: activeDeps, Notes: "superseded but still required by an active configuration"},
			}, nil
		}
		return domain.AuthorityResult{
			CfgID: cfgID, State: domain.AuthorityStateHistorical, Reason: domain.ReasonSuperseded,
			Evidence: domain.AuthorityEvidence{SupersededBy: superseders, Notes: "superseded with no active dependency"},
		}, nil
	}

	// Rule 2 — reachable from the in-force baseline.
	if isActive {
		reason := domain.ReasonReachableFromBaseline
		if info.baseline {
			reason = domain.ReasonBaselineMember
		}
		return domain.AuthorityResult{
			CfgID: cfgID, State: domain.AuthorityStateActive, Reason: reason,
			Evidence: domain.AuthorityEvidence{BaselineMember: info.baseline, ReachableVia: info.via, Notes: "in or required by the in-force baseline"},
		}, nil
	}

	// Rule 3 — exists but nothing active reaches it.
	return domain.AuthorityResult{
		CfgID: cfgID, State: domain.AuthorityStateNonNormative, Reason: domain.ReasonNotReachable,
		Evidence: domain.AuthorityEvidence{Notes: "exists but not reachable from any baseline"},
	}, nil
}

// computeActiveSet is a cycle-safe BFS from the baseline roots following only
// depends/extends edges (supersedes never carries active-reachability — State
// Model §9.1 reach CTE). Uses the M2 edge accessor; does not modify traversal.
func (r *Resolver) computeActiveSet(ctx context.Context) (map[string]reachInfo, error) {
	roots, err := r.data.BaselineRoots(ctx)
	if err != nil {
		return nil, err
	}
	active := make(map[string]reachInfo)
	var queue []string
	for _, root := range roots {
		if _, seen := active[root]; !seen {
			active[root] = reachInfo{baseline: true}
			queue = append(queue, root)
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		edges, err := r.data.EdgesFrom(ctx, n)
		if err != nil {
			return nil, err
		}
		for _, e := range edges {
			if e.EdgeKind != domain.EdgeKindDepends && e.EdgeKind != domain.EdgeKindExtends {
				continue // supersedes does not propagate authority
			}
			if _, seen := active[e.ToCfg]; !seen {
				active[e.ToCfg] = reachInfo{via: n}
				queue = append(queue, e.ToCfg)
			}
		}
	}
	return active, nil
}

// supersededBy returns the sorted cfg_ids whose supersedes edges target cfgID.
func (r *Resolver) supersededBy(ctx context.Context, cfgID string) ([]string, error) {
	in, err := r.data.EdgesTo(ctx, cfgID)
	if err != nil {
		return nil, err
	}
	var superseders []string
	for _, e := range in {
		if e.EdgeKind == domain.EdgeKindSupersedes {
			superseders = append(superseders, e.FromCfg)
		}
	}
	sort.Strings(superseders)
	return superseders, nil
}

// validateSupersession detects cycles in the supersedes relation within scope
// (a broken supersession chain), returning a *ValidationError if found.
func (r *Resolver) validateSupersession(ctx context.Context, scope []string) error {
	inScope := make(map[string]bool, len(scope))
	for _, id := range scope {
		inScope[id] = true
	}
	color := make(map[string]int) // 0=unvisited,1=in-progress,2=done
	var visit func(string) error
	visit = func(n string) error {
		color[n] = 1
		edges, err := r.data.EdgesFrom(ctx, n)
		if err != nil {
			return err
		}
		for _, e := range edges {
			if e.EdgeKind != domain.EdgeKindSupersedes || !inScope[e.ToCfg] {
				continue
			}
			switch color[e.ToCfg] {
			case 1:
				return &ValidationError{Kind: "supersession_cycle", CfgID: e.ToCfg, Detail: "supersedes relation contains a cycle"}
			case 0:
				if err := visit(e.ToCfg); err != nil {
					return err
				}
			}
		}
		color[n] = 2
		return nil
	}
	for _, id := range scope {
		if color[id] == 0 {
			if err := visit(id); err != nil {
				return err
			}
		}
	}
	return nil
}
