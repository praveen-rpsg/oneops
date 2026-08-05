package grouping

import (
	"sort"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

func strp(s string) *string { return &s }

// ---------------------------------------------------------------- deepestDownRoot

// TestDeepestDownRoot_PicksDeepestAmongDownCandidates is E4.2's core payoff at
// the algorithm level (ADR-ALERTING-004 §2): X -> A -> DB, both A and DB down
// (each has its own open alert-incident) — the DEEPEST one, DB, wins, not the
// shallower A. This is the DEEPEST-root selection the story requires,
// mutation-provable: replacing "n.Depth > bestDepth" with ">=" or dropping it
// entirely (first-down-wins) would make this pick "inc-a" instead and fail.
func TestDeepestDownRoot_PicksDeepestAmongDownCandidates(t *testing.T) {
	nodes := []domain.TraversalNode{
		{CfgID: "asset-a", Depth: 1},
		{CfgID: "asset-db", Depth: 2},
	}
	down := map[string]string{"asset-a": "inc-a", "asset-db": "inc-db"}
	got := deepestDownRoot(nodes, down, "asset-x")
	if got == nil || *got != "inc-db" {
		t.Fatalf("deepestDownRoot = %v, want inc-db (the deeper node)", got)
	}
}

// TestDeepestDownRoot_TieBreakLexicographicallySmallestAtMaxDepth pins the
// documented tie-break: two down nodes share the maximum depth, and the
// lexicographically-smaller asset_id wins because nodes is already ordered
// (depth, asset_id) ascending and the scan only replaces its best on a
// STRICTLY greater depth. Feeding nodes out of that contract order would be a
// caller bug, not this function's; the test constructs them already sorted
// exactly as RecursiveDependenciesOfTypes guarantees.
func TestDeepestDownRoot_TieBreakLexicographicallySmallestAtMaxDepth(t *testing.T) {
	nodes := []domain.TraversalNode{
		{CfgID: "asset-a", Depth: 2},
		{CfgID: "asset-b", Depth: 2},
	}
	down := map[string]string{"asset-a": "inc-a", "asset-b": "inc-b"}
	got := deepestDownRoot(nodes, down, "asset-x")
	if got == nil || *got != "inc-a" {
		t.Fatalf("deepestDownRoot = %v, want inc-a (lexicographically first at the shared max depth)", got)
	}
}

// TestDeepestDownRoot_ExcludesOwnAssetID is the last line of defense against a
// self-root: even if the (cycle-safe) traversal somehow returned the walk's
// own starting asset, it must never be handed back as a candidate — even when
// it would otherwise WIN on depth. asset-x is placed at the greatest depth
// (5) precisely so this is mutation-provable: deleting the "n.CfgID ==
// ownAssetID" guard makes deepestDownRoot return inc-x instead, because
// without the guard asset-x's depth beats asset-a's.
func TestDeepestDownRoot_ExcludesOwnAssetID(t *testing.T) {
	nodes := []domain.TraversalNode{
		{CfgID: "asset-a", Depth: 1},
		{CfgID: "asset-x", Depth: 5},
	}
	down := map[string]string{"asset-x": "inc-x", "asset-a": "inc-a"}
	got := deepestDownRoot(nodes, down, "asset-x")
	if got == nil || *got != "inc-a" {
		t.Fatalf("deepestDownRoot = %v, want inc-a (asset-x must be excluded even though it is 'down' and deeper)", got)
	}
}

// TestDeepestDownRoot_IgnoresNonDownNodes proves a shallower node is skipped
// when it carries no open incident of its own — only actually-down nodes are
// candidates, direction and depth aside.
func TestDeepestDownRoot_IgnoresNonDownNodes(t *testing.T) {
	nodes := []domain.TraversalNode{
		{CfgID: "asset-a", Depth: 1}, // not down
		{CfgID: "asset-db", Depth: 2},
	}
	down := map[string]string{"asset-db": "inc-db"}
	got := deepestDownRoot(nodes, down, "asset-x")
	if got == nil || *got != "inc-db" {
		t.Fatalf("deepestDownRoot = %v, want inc-db", got)
	}
}

// TestDeepestDownRoot_NoDownDependencyReturnsNil is the "root or standalone"
// case: nothing in the closure is down, so there is no candidate root at all.
func TestDeepestDownRoot_NoDownDependencyReturnsNil(t *testing.T) {
	nodes := []domain.TraversalNode{{CfgID: "asset-a", Depth: 1}}
	got := deepestDownRoot(nodes, map[string]string{}, "asset-x")
	if got != nil {
		t.Fatalf("deepestDownRoot = %v, want nil", *got)
	}
}

// TestDeepestDownRoot_EmptyClosureReturnsNil covers a leaf asset with no
// dependencies at all — the shape a root itself always presents.
func TestDeepestDownRoot_EmptyClosureReturnsNil(t *testing.T) {
	got := deepestDownRoot(nil, map[string]string{"asset-x": "inc-x"}, "asset-x")
	if got != nil {
		t.Fatalf("deepestDownRoot = %v, want nil for an empty closure", *got)
	}
}

// ---------------------------------------------------------------- resolveFinalRoots

// TestResolveFinalRoots_NoCycleKeepsCandidates is the ordinary, non-pathological
// case this function passes through unchanged: every incident's independently-
// computed candidate is acyclic (the normal case per ADR-ALERTING-004 §4 —
// candidate is computed from each incident's OWN full closure, so it can
// never legitimately chain through another incident's candidate).
func TestResolveFinalRoots_NoCycleKeepsCandidates(t *testing.T) {
	candidate := map[string]*string{
		"inc-x":  strp("inc-db"),
		"inc-a":  strp("inc-db"),
		"inc-db": nil,
	}
	final := resolveFinalRoots(candidate)
	if got := final["inc-x"]; got == nil || *got != "inc-db" {
		t.Errorf("inc-x = %v, want inc-db", got)
	}
	if got := final["inc-a"]; got == nil || *got != "inc-db" {
		t.Errorf("inc-a = %v, want inc-db", got)
	}
	if got := final["inc-db"]; got != nil {
		t.Errorf("inc-db = %v, want nil", *got)
	}
}

// TestResolveFinalRoots_SelfPointerNeverPersisted is the last-line-of-defense
// case (structurally impossible from deepestDownRoot, per its own doc
// comment): even a self-pointer sneaking into candidate must resolve to nil,
// never survive as a stored self-reference.
func TestResolveFinalRoots_SelfPointerNeverPersisted(t *testing.T) {
	candidate := map[string]*string{"inc-a": strp("inc-a")}
	final := resolveFinalRoots(candidate)
	if got := final["inc-a"]; got != nil {
		t.Fatalf("inc-a = %v, want nil (a self-pointer must never be persisted)", *got)
	}
}

// TestResolveFinalRoots_TwoCycleBrokenAtLexicographicallySmallest is the
// documented cycle-break rule (ADR-ALERTING-004 §5): a genuine CMDB cycle (two
// currently-down assets each depending on the other) cannot resolve to one
// canonical deepest root, so the function breaks it deterministically by
// anchoring the lexicographically-smallest incidentID in the cycle to nil —
// self-healing and idempotent because it is recomputed identically every
// tick, never a stored decision.
func TestResolveFinalRoots_TwoCycleBrokenAtLexicographicallySmallest(t *testing.T) {
	candidate := map[string]*string{
		"inc-a": strp("inc-b"),
		"inc-b": strp("inc-a"),
	}
	final := resolveFinalRoots(candidate)
	if got := final["inc-a"]; got != nil {
		t.Fatalf("inc-a = %v, want nil (the lexicographically-smallest member anchors the break)", *got)
	}
	if got := final["inc-b"]; got == nil || *got != "inc-a" {
		t.Fatalf("inc-b = %v, want inc-a (keeps its own computed candidate)", got)
	}
}

// TestResolveFinalRoots_ChainIntoCycleKeepsOutsideNodesUnaffected proves the
// cycle-break is confined to the cycle itself: an incident whose candidate
// merely POINTS INTO a cyclic structure from outside is not itself cyclic and
// must keep its own computed candidate untouched.
func TestResolveFinalRoots_ChainIntoCycleKeepsOutsideNodesUnaffected(t *testing.T) {
	candidate := map[string]*string{
		"inc-z": strp("inc-a"), // points into the cycle below, not part of it
		"inc-a": strp("inc-b"),
		"inc-b": strp("inc-a"),
	}
	final := resolveFinalRoots(candidate)
	if got := final["inc-z"]; got == nil || *got != "inc-a" {
		t.Errorf("inc-z = %v, want inc-a (unaffected by the cycle it merely points into)", got)
	}
	if got := final["inc-a"]; got != nil {
		t.Errorf("inc-a = %v, want nil (cycle anchor)", *got)
	}
	if got := final["inc-b"]; got == nil || *got != "inc-a" {
		t.Errorf("inc-b = %v, want inc-a", got)
	}
}

// TestResolveFinalRoots_DeterministicAcrossRuns proves the pure function is
// stable given unchanged input — the property the reconciler's idempotence
// relies on: re-running against an unchanged candidate map reproduces the
// same break point every time.
func TestResolveFinalRoots_DeterministicAcrossRuns(t *testing.T) {
	candidate := map[string]*string{
		"inc-a": strp("inc-b"),
		"inc-b": strp("inc-c"),
		"inc-c": strp("inc-a"),
	}
	first := resolveFinalRoots(candidate)
	second := resolveFinalRoots(candidate)
	ids := make([]string, 0, len(candidate))
	for id := range candidate {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		a, b := first[id], second[id]
		if (a == nil) != (b == nil) || (a != nil && b != nil && *a != *b) {
			t.Errorf("%s: first=%v second=%v, want identical across runs", id, a, b)
		}
	}
}
