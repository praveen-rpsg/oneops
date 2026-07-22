package authority

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// fakeGraph is an in-memory domain.AuthorityGraph. Tests declare nodes, edges,
// and baseline roots; the fake derives EdgesFrom/EdgesTo/RecursiveDependencies
// consistently (RecursiveDependencies follows all edge kinds, like M2).
type fakeGraph struct {
	nodes    map[string]bool
	edges    []*domain.DependencyEdge
	baseline []string
}

func g() *fakeGraph { return &fakeGraph{nodes: map[string]bool{}} }

func (f *fakeGraph) node(ids ...string) *fakeGraph {
	for _, id := range ids {
		f.nodes[id] = true
	}
	return f
}

func (f *fakeGraph) base(ids ...string) *fakeGraph {
	f.node(ids...)
	f.baseline = append(f.baseline, ids...)
	return f
}

func (f *fakeGraph) edge(from, to string, kind domain.EdgeKind) *fakeGraph {
	f.node(from, to)
	f.edges = append(f.edges, &domain.DependencyEdge{FromCfg: from, ToCfg: to, EdgeKind: kind})
	return f
}

func (f *fakeGraph) BaselineRoots(context.Context) ([]string, error) {
	out := append([]string{}, f.baseline...)
	sort.Strings(out)
	return out, nil
}

func (f *fakeGraph) ObjectExists(_ context.Context, id string) (bool, error) {
	return f.nodes[id], nil
}

func (f *fakeGraph) EdgesFrom(_ context.Context, id string) ([]*domain.DependencyEdge, error) {
	var out []*domain.DependencyEdge
	for _, e := range f.edges {
		if e.FromCfg == id {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeGraph) EdgesTo(_ context.Context, id string) ([]*domain.DependencyEdge, error) {
	var out []*domain.DependencyEdge
	for _, e := range f.edges {
		if e.ToCfg == id {
			out = append(out, e)
		}
	}
	return out, nil
}

func (f *fakeGraph) RecursiveDependencies(_ context.Context, id string) ([]domain.TraversalNode, error) {
	depth := map[string]int{}
	var queue []string
	push := func(from string, d int) {
		for _, e := range f.edges {
			if e.FromCfg == from {
				if _, ok := depth[e.ToCfg]; !ok {
					depth[e.ToCfg] = d
					queue = append(queue, e.ToCfg)
				}
			}
		}
	}
	push(id, 1)
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		push(n, depth[n]+1)
	}
	var out []domain.TraversalNode
	for k, d := range depth {
		out = append(out, domain.TraversalNode{CfgID: k, Depth: d})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CfgID < out[j].CfgID })
	return out, nil
}

// --- helpers -----------------------------------------------------------------

func resolve(t *testing.T, f *fakeGraph, id string) domain.AuthorityResult {
	t.Helper()
	res, err := NewResolver(f).ResolveAuthority(context.Background(), id)
	if err != nil {
		t.Fatalf("resolve %s: %v", id, err)
	}
	return res
}

func assertState(t *testing.T, res domain.AuthorityResult, state domain.AuthorityState, reason domain.AuthorityReason) {
	t.Helper()
	if res.State != state {
		t.Fatalf("%s: state = %s, want %s (reason %s)", res.CfgID, res.State, state, res.Reason)
	}
	if res.Reason != reason {
		t.Fatalf("%s: reason = %s, want %s", res.CfgID, res.Reason, reason)
	}
}

// --- tests -------------------------------------------------------------------

// Baseline B; B depends on A; C unrelated. Expect B & A Active, C Non-Normative,
// missing X Unknown.
func TestSingleBaseline(t *testing.T) {
	f := g().base("B").edge("B", "A", domain.EdgeKindDepends).node("C")
	assertState(t, resolve(t, f, "B"), domain.AuthorityStateActive, domain.ReasonBaselineMember)
	a := resolve(t, f, "A")
	assertState(t, a, domain.AuthorityStateActive, domain.ReasonReachableFromBaseline)
	if a.Evidence.ReachableVia != "B" {
		t.Errorf("A reachable via = %q, want B", a.Evidence.ReachableVia)
	}
	assertState(t, resolve(t, f, "C"), domain.AuthorityStateNonNormative, domain.ReasonNotReachable)
	assertState(t, resolve(t, f, "X"), domain.AuthorityStateUnknown, domain.ReasonObjectNotFound)
}

// Two baselines both depending on a shared node.
func TestMultipleBaselines(t *testing.T) {
	f := g().base("B1", "B2").edge("B1", "S", domain.EdgeKindDepends).edge("B2", "S", domain.EdgeKindExtends)
	assertState(t, resolve(t, f, "B1"), domain.AuthorityStateActive, domain.ReasonBaselineMember)
	assertState(t, resolve(t, f, "B2"), domain.AuthorityStateActive, domain.ReasonBaselineMember)
	assertState(t, resolve(t, f, "S"), domain.AuthorityStateActive, domain.ReasonReachableFromBaseline)
}

// New supersedes Old. Old → Historical; New (baseline) → Active.
func TestSupersession(t *testing.T) {
	f := g().base("New").edge("New", "Old", domain.EdgeKindSupersedes)
	old := resolve(t, f, "Old")
	assertState(t, old, domain.AuthorityStateHistorical, domain.ReasonSuperseded)
	if len(old.Evidence.SupersededBy) != 1 || old.Evidence.SupersededBy[0] != "New" {
		t.Errorf("Old superseded_by = %v, want [New]", old.Evidence.SupersededBy)
	}
	assertState(t, resolve(t, f, "New"), domain.AuthorityStateActive, domain.ReasonBaselineMember)
}

// A superseded by B, B superseded by C(baseline): both A and B Historical.
func TestHistoricalChain(t *testing.T) {
	f := g().base("C").edge("C", "B", domain.EdgeKindSupersedes).edge("B", "A", domain.EdgeKindSupersedes)
	assertState(t, resolve(t, f, "A"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
	assertState(t, resolve(t, f, "B"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
	assertState(t, resolve(t, f, "C"), domain.AuthorityStateActive, domain.ReasonBaselineMember)
}

// A superseded dependency does NOT keep its own dependencies Active (supersedes
// never carries reachability): Old→P depends, but Old is superseded → P is
// Non-Normative (nothing active reaches it).
func TestSupersedesDoesNotPropagateReachability(t *testing.T) {
	f := g().base("New").edge("New", "Old", domain.EdgeKindSupersedes).edge("Old", "P", domain.EdgeKindDepends)
	assertState(t, resolve(t, f, "Old"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
	assertState(t, resolve(t, f, "P"), domain.AuthorityStateNonNormative, domain.ReasonNotReachable)
}

func TestNonNormative(t *testing.T) {
	f := g().base("B").node("Free")
	assertState(t, resolve(t, f, "Free"), domain.AuthorityStateNonNormative, domain.ReasonNotReachable)
}

// No baseline at all → everything that exists is Non-Normative (unless superseded).
func TestMissingBaseline(t *testing.T) {
	f := g().node("A", "B").edge("A", "B", domain.EdgeKindDepends)
	assertState(t, resolve(t, f, "A"), domain.AuthorityStateNonNormative, domain.ReasonNotReachable)
	assertState(t, resolve(t, f, "B"), domain.AuthorityStateNonNormative, domain.ReasonNotReachable)
}

// Dependency cycle (depends) is authority-safe: resolution terminates, both Active.
func TestDependencyCycleIsSafe(t *testing.T) {
	f := g().base("B").edge("B", "A", domain.EdgeKindDepends).edge("A", "B", domain.EdgeKindDepends)
	assertState(t, resolve(t, f, "A"), domain.AuthorityStateActive, domain.ReasonReachableFromBaseline)
	assertState(t, resolve(t, f, "B"), domain.AuthorityStateActive, domain.ReasonBaselineMember)
}

// Broken supersession chain (A supersedes B, B supersedes A) → ValidationError.
func TestBrokenSupersessionChain(t *testing.T) {
	f := g().node("A", "B").edge("A", "B", domain.EdgeKindSupersedes).edge("B", "A", domain.EdgeKindSupersedes)
	_, err := NewResolver(f).ResolveGraph(context.Background(), "A")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Kind != "supersession_cycle" {
		t.Fatalf("expected supersession_cycle ValidationError, got %v", err)
	}
}

// Baseline member that is also superseded is contradictory → Unknown.
func TestUnknownInconsistentState(t *testing.T) {
	f := g().base("B").edge("X", "B", domain.EdgeKindSupersedes).node("X")
	res := resolve(t, f, "B")
	assertState(t, res, domain.AuthorityStateUnknown, domain.ReasonInconsistentBaselineSuperseded)
	if !res.Evidence.BaselineMember {
		t.Error("expected BaselineMember evidence on the inconsistency")
	}
}

// Batch resolution preserves input order and shares one baseline computation.
func TestResolveBatch(t *testing.T) {
	f := g().base("B").edge("B", "A", domain.EdgeKindDepends).edge("N", "A2", domain.EdgeKindSupersedes).node("Free")
	ids := []string{"B", "A", "A2", "Free", "missing"}
	out, err := NewResolver(f).ResolveBatch(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != len(ids) {
		t.Fatalf("batch len = %d, want %d", len(out), len(ids))
	}
	for i, id := range ids {
		if out[i].CfgID != id {
			t.Fatalf("batch order broken at %d: %s != %s", i, out[i].CfgID, id)
		}
	}
	want := []domain.AuthorityState{
		domain.AuthorityStateActive, domain.AuthorityStateActive, domain.AuthorityStateHistorical,
		domain.AuthorityStateNonNormative, domain.AuthorityStateUnknown,
	}
	for i, w := range want {
		if out[i].State != w {
			t.Errorf("%s: state = %s, want %s", out[i].CfgID, out[i].State, w)
		}
	}
}

// Graph resolution resolves root + transitive deps, ordered by cfg_id.
func TestResolveGraph(t *testing.T) {
	f := g().base("B").edge("B", "A", domain.EdgeKindDepends).edge("A", "D", domain.EdgeKindDepends)
	out, err := NewResolver(f).ResolveGraph(context.Background(), "B")
	if err != nil {
		t.Fatal(err)
	}
	// scope = {A, B, D} sorted
	if len(out) != 3 {
		t.Fatalf("graph scope = %d, want 3: %+v", len(out), out)
	}
	byID := map[string]domain.AuthorityResult{}
	for _, r := range out {
		byID[r.CfgID] = r
	}
	for _, id := range []string{"A", "B", "D"} {
		if byID[id].State != domain.AuthorityStateActive {
			t.Errorf("%s state = %s, want ACTIVE", id, byID[id].State)
		}
	}
	// deterministic ordering
	for i := 1; i < len(out); i++ {
		if out[i-1].CfgID > out[i].CfgID {
			t.Fatalf("results not ordered by cfg_id: %v", out)
		}
	}
}

func TestDeterministicResults(t *testing.T) {
	f := g().base("B").edge("B", "A", domain.EdgeKindDepends).edge("A", "C", domain.EdgeKindDepends)
	first := resolve(t, f, "C")
	for i := 0; i < 5; i++ {
		got := resolve(t, f, "C")
		if got.State != first.State || got.Reason != first.Reason ||
			got.Evidence.ReachableVia != first.Evidence.ReachableVia ||
			got.Evidence.BaselineMember != first.Evidence.BaselineMember {
			t.Fatal("resolution is not deterministic")
		}
	}
}

func TestAuthorityStateValid(t *testing.T) {
	for _, s := range []domain.AuthorityState{domain.AuthorityStateActive, domain.AuthorityStateHistorical, domain.AuthorityStateNonNormative, domain.AuthorityStateUnknown} {
		if !s.Valid() {
			t.Errorf("%s should be valid", s)
		}
	}
	if domain.AuthorityState("bogus").Valid() {
		t.Error("bogus should be invalid")
	}
}
