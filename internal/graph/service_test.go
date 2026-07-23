package graph

import (
	"context"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// fakeTraversal is an in-memory domain.GraphTraversal for deterministic,
// DB-free service tests.
type fakeTraversal struct {
	deps       []domain.TraversalNode
	dependents []domain.TraversalNode
	cycles     []domain.GraphPath
	err        error
}

func (f *fakeTraversal) Dependencies(context.Context, string) ([]string, error) { return nil, f.err }
func (f *fakeTraversal) Dependents(context.Context, string) ([]string, error)   { return nil, f.err }
func (f *fakeTraversal) RecursiveDependencies(context.Context, string) ([]domain.TraversalNode, error) {
	return f.deps, f.err
}
func (f *fakeTraversal) RecursiveDependents(context.Context, string) ([]domain.TraversalNode, error) {
	return f.dependents, f.err
}
func (f *fakeTraversal) CyclePaths(context.Context, string, domain.Direction) ([]domain.GraphPath, error) {
	return f.cycles, f.err
}

func TestWalkDependenciesWrapsResult(t *testing.T) {
	repo := &fakeTraversal{deps: []domain.TraversalNode{{CfgID: "b", Depth: 1}, {CfgID: "c", Depth: 2}}}
	svc := NewService(repo)

	res, err := svc.WalkDependencies(context.Background(), "a")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if res.Root != "a" || res.Direction != domain.DirectionDependencies {
		t.Fatalf("unexpected result header: %+v", res)
	}
	if res.Count() != 2 || res.MaxDepth() != 2 {
		t.Fatalf("count/depth = %d/%d", res.Count(), res.MaxDepth())
	}
}

func TestWalkDependentsUsesReverse(t *testing.T) {
	repo := &fakeTraversal{dependents: []domain.TraversalNode{{CfgID: "z", Depth: 1}}}
	svc := NewService(repo)

	res, err := svc.WalkDependents(context.Background(), "a")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if res.Direction != domain.DirectionDependents || res.Count() != 1 || res.IDs()[0] != "z" {
		t.Fatalf("unexpected reverse result: %+v", res)
	}
}

func TestDetectCyclesCanonicalizesAndDeduplicates(t *testing.T) {
	// Two rotations of the same 2-cycle plus a direct self-loop.
	repo := &fakeTraversal{cycles: []domain.GraphPath{
		{Nodes: []string{"root", "b", "c", "b"}}, // loop b->c->b
		{Nodes: []string{"root", "c", "b", "c"}}, // same loop, rotated
		{Nodes: []string{"x", "x"}},              // direct self-loop
	}}
	svc := NewService(repo)

	cycles, err := svc.DetectCycles(context.Background(), "root")
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	// Expect two distinct cycles: b<->c and x self-loop.
	if len(cycles) != 2 {
		t.Fatalf("expected 2 distinct cycles, got %d: %v", len(cycles), cycles)
	}
	// Deterministic, canonical closed paths, sorted by string.
	if cycles[0].Path.String() != "b -> c -> b" {
		t.Errorf("cycle[0] = %q, want b -> c -> b", cycles[0].Path.String())
	}
	if cycles[1].Path.String() != "x -> x" {
		t.Errorf("cycle[1] = %q, want x -> x", cycles[1].Path.String())
	}

	// Determinism: repeated calls yield identical output.
	again, _ := svc.DetectCycles(context.Background(), "root")
	if len(again) != len(cycles) || again[0].Path.String() != cycles[0].Path.String() {
		t.Error("DetectCycles output is not deterministic")
	}
}

func TestDetectCyclesNoneWhenAcyclic(t *testing.T) {
	svc := NewService(&fakeTraversal{})
	cycles, err := svc.DetectCycles(context.Background(), "a")
	if err != nil || len(cycles) != 0 {
		t.Fatalf("expected no cycles, got %v (%v)", cycles, err)
	}
}
