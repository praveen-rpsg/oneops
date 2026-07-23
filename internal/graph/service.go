// Package graph provides read-only dependency-graph traversal on top of the
// M2.1 persistence layer. It exposes forward/reverse traversal and cycle
// detection only — no authority, lifecycle, baseline, caching, or events.
package graph

import (
	"context"
	"sort"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/rpsg/oneops/internal/domain"
)

var tracer = otel.Tracer("github.com/rpsg/oneops/internal/graph")

// Service performs dependency-graph traversal over a domain.GraphTraversal.
type Service struct {
	repo domain.GraphTraversal
}

// NewService builds a graph service over the given traversal repository.
func NewService(repo domain.GraphTraversal) *Service {
	return &Service{repo: repo}
}

// WalkDependencies returns the transitive forward closure (root → dependencies).
func (s *Service) WalkDependencies(ctx context.Context, root string) (*domain.TraversalResult, error) {
	return s.walk(ctx, root, domain.DirectionDependencies)
}

// WalkDependents returns the transitive reverse closure (root ← dependents).
func (s *Service) WalkDependents(ctx context.Context, root string) (*domain.TraversalResult, error) {
	return s.walk(ctx, root, domain.DirectionDependents)
}

func (s *Service) walk(ctx context.Context, root string, dir domain.Direction) (*domain.TraversalResult, error) {
	ctx, span := tracer.Start(ctx, "GraphService.Walk", trace.WithAttributes(
		attribute.String("graph.root", root),
		attribute.String("graph.direction", string(dir)),
	))
	defer span.End()

	var (
		nodes []domain.TraversalNode
		err   error
	)
	if dir == domain.DirectionDependents {
		nodes, err = s.repo.RecursiveDependents(ctx, root)
	} else {
		nodes, err = s.repo.RecursiveDependencies(ctx, root)
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "walk")
		return nil, err
	}

	res := &domain.TraversalResult{Root: root, Direction: dir, Nodes: nodes}
	span.SetAttributes(
		attribute.Int("graph.node_count", res.Count()),
		attribute.Int("graph.max_depth", res.MaxDepth()),
	)
	return res, nil
}

// DetectCycles reports the cycles reachable from root by following dependency
// edges. Each cycle is returned as a canonical closed path. Cycles are not
// repaired. The output is deterministic (deduplicated and sorted).
func (s *Service) DetectCycles(ctx context.Context, root string) ([]domain.Cycle, error) {
	ctx, span := tracer.Start(ctx, "GraphService.DetectCycles", trace.WithAttributes(
		attribute.String("graph.root", root),
	))
	defer span.End()

	paths, err := s.repo.CyclePaths(ctx, root, domain.DirectionDependencies)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "detect cycles")
		return nil, err
	}

	cycles := canonicalCycles(paths)
	span.SetAttributes(attribute.Int("graph.cycle_count", len(cycles)))
	return cycles, nil
}

// canonicalCycles extracts, canonicalizes, deduplicates, and sorts the cycles
// from a set of closing paths. A closing path ends with a node that appeared
// earlier; the loop between the two occurrences is the cycle.
func canonicalCycles(paths []domain.GraphPath) []domain.Cycle {
	seen := map[string]domain.Cycle{}
	for _, p := range paths {
		loop := canonicalLoop(p.Nodes)
		if len(loop) == 0 {
			continue
		}
		key := strings.Join(loop, ",")
		if _, ok := seen[key]; !ok {
			closed := append(append([]string{}, loop...), loop[0])
			seen[key] = domain.Cycle{Path: domain.GraphPath{Nodes: closed}}
		}
	}
	out := make([]domain.Cycle, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Path.String() < out[j].Path.String()
	})
	return out
}

// canonicalLoop returns the cycle's unique nodes, rotated to begin at the
// lexicographically smallest node so that equivalent rotations collapse.
func canonicalLoop(path []string) []string {
	if len(path) < 2 {
		return nil
	}
	last := path[len(path)-1]
	first := -1
	for i, n := range path {
		if n == last {
			first = i
			break
		}
	}
	if first < 0 || first == len(path)-1 {
		return nil
	}
	loop := path[first : len(path)-1] // unique nodes of the cycle
	if len(loop) == 0 {
		return nil
	}
	minIdx := 0
	for i := range loop {
		if loop[i] < loop[minIdx] {
			minIdx = i
		}
	}
	rotated := make([]string, 0, len(loop))
	rotated = append(rotated, loop[minIdx:]...)
	rotated = append(rotated, loop[:minIdx]...)
	return rotated
}
