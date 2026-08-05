package grouping

import (
	"context"
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// TestReconciler_GroupsCollateralUnderDeepestRoot is E4.2's core payoff,
// composed at the reconciler level (the story's "grouping bites" and
// "DEEPEST-root selection" scenarios are the same fixture): DB is down with
// its own incident; A and X each transitively depend on DB (X -> A -> DB) and
// each have their own open alert-incident. Both X and A must be grouped under
// DB's incident — the DEEPEST down dependency in X's full closure — not under
// the shallower A, and this must come out FLAT (X points straight at DB, not
// at A) because RecursiveDependenciesOfTypes(X) already returns X's full
// transitive closure.
func TestReconciler_GroupsCollateralUnderDeepestRoot(t *testing.T) {
	store := newFakeStore()
	store.addIncident("t1", "inc-x", "asset-x")
	store.addIncident("t1", "inc-a", "asset-a")
	store.addIncident("t1", "inc-db", "asset-db")

	graph := newFakeGraph()
	graph.set("t1", "asset-x", []domain.TraversalNode{{CfgID: "asset-a", Depth: 1}, {CfgID: "asset-db", Depth: 2}})
	graph.set("t1", "asset-a", []domain.TraversalNode{{CfgID: "asset-db", Depth: 1}})
	graph.set("t1", "asset-db", nil)

	r := NewReconciler(store, graph, NopMetrics{}, nil, Config{})
	r.RunOnce(context.Background())

	if got := store.root("t1", "inc-x"); got == nil || *got != "inc-db" {
		t.Fatalf("inc-x root = %v, want inc-db (the deepest down dependency, not inc-a)", got)
	}
	if got := store.root("t1", "inc-a"); got == nil || *got != "inc-db" {
		t.Fatalf("inc-a root = %v, want inc-db", got)
	}
	if got := store.root("t1", "inc-db"); got != nil {
		t.Fatalf("inc-db (the deepest root itself) root = %v, want nil", *got)
	}
}

// TestReconciler_DirectionCorrectness is the serious-bug guard the story calls
// out by name, at the reconciler level: Y depends on X (Y -> X), not the
// reverse. Y being down must root under X when X is also down, but X (the
// thing Y needs) must NEVER be rooted under Y merely because Y also has an
// incident — that would require walking Y's DEPENDENTS, which this feature
// never does.
func TestReconciler_DirectionCorrectness(t *testing.T) {
	store := newFakeStore()
	store.addIncident("t1", "inc-x", "asset-x")
	store.addIncident("t1", "inc-y", "asset-y")

	graph := newFakeGraph()
	// asset-x depends on nothing (it does NOT depend on asset-y).
	graph.set("t1", "asset-x", nil)
	// asset-y depends on asset-x — the real, forward direction.
	graph.set("t1", "asset-y", []domain.TraversalNode{{CfgID: "asset-x", Depth: 1}})

	r := NewReconciler(store, graph, NopMetrics{}, nil, Config{})
	r.RunOnce(context.Background())

	if got := store.root("t1", "inc-x"); got != nil {
		t.Fatalf("inc-x (X, which Y depends ON) root = %v, want nil — a dependent's own incident must never "+
			"become the thing it depends on's root", *got)
	}
	if got := store.root("t1", "inc-y"); got == nil || *got != "inc-x" {
		t.Fatalf("inc-y root = %v, want inc-x (Y really does depend on X)", got)
	}
}

// TestReconciler_SelfHealingAndIdempotent is both the self-healing and
// idempotence proof in one fixture: DB is the deepest root for X and A; DB's
// own incident then resolves (the down signal disappears), and the very next
// pass must re-root X and A to A — the next-still-down node in X's own
// closure — with A itself clearing to nil. A further pass against that
// unchanged state must write nothing at all.
func TestReconciler_SelfHealingAndIdempotent(t *testing.T) {
	store := newFakeStore()
	store.addIncident("t1", "inc-x", "asset-x")
	store.addIncident("t1", "inc-a", "asset-a")
	store.addIncident("t1", "inc-db", "asset-db")

	graph := newFakeGraph()
	graph.set("t1", "asset-x", []domain.TraversalNode{{CfgID: "asset-a", Depth: 1}, {CfgID: "asset-db", Depth: 2}})
	graph.set("t1", "asset-a", []domain.TraversalNode{{CfgID: "asset-db", Depth: 1}})
	graph.set("t1", "asset-db", nil)

	r := NewReconciler(store, graph, NopMetrics{}, nil, Config{})
	r.RunOnce(context.Background())
	if got := store.root("t1", "inc-x"); got == nil || *got != "inc-db" {
		t.Fatalf("setup: inc-x root = %v, want inc-db", got)
	}
	writesAfterFirst := store.writeCount()

	// The deepest root's own incident resolves — it stops being "down".
	store.resolve("t1", "inc-db")

	r.RunOnce(context.Background())
	if got := store.root("t1", "inc-x"); got == nil || *got != "inc-a" {
		t.Fatalf("after the root resolved: inc-x root = %v, want inc-a (re-rooted to the still-down dependency)", got)
	}
	if got := store.root("t1", "inc-a"); got != nil {
		t.Fatalf("inc-a root = %v, want nil (it is now the deepest down node in its own closure)", *got)
	}
	writesAfterSecond := store.writeCount()
	if writesAfterSecond == writesAfterFirst {
		t.Fatal("expected writes on the self-healing pass; none were recorded")
	}

	// A third pass against unchanged state must be a pure no-op.
	r.RunOnce(context.Background())
	if got := store.writeCount(); got != writesAfterSecond {
		t.Errorf("idempotent re-run performed %d additional writes, want 0 on unchanged state", got-writesAfterSecond)
	}
}

// TestReconciler_NoSelfOrCyclicRootPointer proves the reconciler end-to-end
// against a genuine CMDB cycle (A depends_on B, B depends_on A, both down):
// the persisted result must never contain a pointer cycle or a self-pointer —
// resolveFinalRoots's own unit tests prove the pure function; this proves the
// reconciler actually calls it and persists what it returns.
func TestReconciler_NoSelfOrCyclicRootPointer(t *testing.T) {
	store := newFakeStore()
	store.addIncident("t1", "inc-a", "asset-a")
	store.addIncident("t1", "inc-b", "asset-b")

	graph := newFakeGraph()
	graph.set("t1", "asset-a", []domain.TraversalNode{{CfgID: "asset-b", Depth: 1}})
	graph.set("t1", "asset-b", []domain.TraversalNode{{CfgID: "asset-a", Depth: 1}})

	r := NewReconciler(store, graph, NopMetrics{}, nil, Config{})
	r.RunOnce(context.Background())

	rootA := store.root("t1", "inc-a")
	rootB := store.root("t1", "inc-b")
	if rootA != nil {
		t.Errorf("inc-a root = %v, want nil (deterministic cycle-break anchor: lexicographically smallest)", *rootA)
	}
	if rootB == nil || *rootB != "inc-a" {
		t.Fatalf("inc-b root = %v, want inc-a", rootB)
	}
	if rootA != nil && *rootA == "inc-a" {
		t.Fatal("inc-a points at itself")
	}
	if rootB != nil && *rootB == "inc-b" {
		t.Fatal("inc-b points at itself")
	}
}

// TestReconciler_TenantsAreIndependentEvenWithSharedAssetIDs is the tenant-
// isolation proof at the reconciler level (structural isolation is documented
// on RunOnce itself): two tenants share the exact same asset_id strings but
// have entirely different topologies. Only the tenant whose OWN topology
// declares the dependency may be grouped; the other must not be, and every
// graph call must carry an actual tenant scope (never an empty one), proving
// the reconciler re-derives domain.WithTenant per tenant rather than reusing
// one context across the whole sweep.
func TestReconciler_TenantsAreIndependentEvenWithSharedAssetIDs(t *testing.T) {
	const sharedX = "asset-shared-x"
	const sharedY = "asset-shared-y"

	store := newFakeStore()
	store.addIncident("tenant-a", "inc-a-x", sharedX)
	store.addIncident("tenant-a", "inc-a-y", sharedY)
	store.addIncident("tenant-b", "inc-b-x", sharedX)
	store.addIncident("tenant-b", "inc-b-y", sharedY)

	graph := newFakeGraph()
	// tenant-a: X really does depend on Y.
	graph.set("tenant-a", sharedX, []domain.TraversalNode{{CfgID: sharedY, Depth: 1}})
	graph.set("tenant-a", sharedY, nil)
	// tenant-b: no dependency edge at all between the identically-named assets.
	graph.set("tenant-b", sharedX, nil)
	graph.set("tenant-b", sharedY, nil)

	r := NewReconciler(store, graph, NopMetrics{}, nil, Config{})
	r.RunOnce(context.Background())

	if got := store.root("tenant-a", "inc-a-x"); got == nil || *got != "inc-a-y" {
		t.Fatalf("tenant-a inc-a-x root = %v, want inc-a-y", got)
	}
	if got := store.root("tenant-b", "inc-b-x"); got != nil {
		t.Fatalf("tenant-b inc-b-x root = %v, want nil — tenant B's own topology has no edge; a leak from "+
			"tenant A's identically-named asset would wrongly set this", *got)
	}

	for _, c := range graph.calls {
		if c.tenantID == "" {
			t.Fatalf("a graph call for asset %q carried no tenant scope at all — the reconciler must bind "+
				"domain.WithTenant per tenant, never call the graph unscoped", c.assetID)
		}
	}
}

// TestReconciler_GraphErrorLeavesStoredValueUntouched mirrors the alerting
// package's "duplicate over silent loss" doctrine: a dependency-walk error
// must never write a guessed value; the next pass, once the walk succeeds,
// retries and converges.
func TestReconciler_GraphErrorLeavesStoredValueUntouched(t *testing.T) {
	store := newFakeStore()
	store.addIncident("t1", "inc-x", "asset-x")
	store.addIncident("t1", "inc-db", "asset-db")

	graph := newFakeGraph()
	graph.set("t1", "asset-x", []domain.TraversalNode{{CfgID: "asset-db", Depth: 1}})
	graph.set("t1", "asset-db", nil)
	graph.err = errors.New("injected: dependency walk unavailable")

	r := NewReconciler(store, graph, NopMetrics{}, nil, Config{})
	r.RunOnce(context.Background())
	if got := store.root("t1", "inc-x"); got != nil {
		t.Fatalf("root after a walk error = %v, want nil (unchanged — a guess must never be written)", *got)
	}

	graph.err = nil
	r.RunOnce(context.Background())
	if got := store.root("t1", "inc-x"); got == nil || *got != "inc-db" {
		t.Fatalf("root after retry = %v, want inc-db", got)
	}
}

// TestReconciler_NeverTouchesAnythingButRootIncidentID proves the "grouping
// never mutates incident STATE" constraint structurally: grouping.Store's
// only write method, SetRootIncidentID, cannot express a status change at
// all — its signature carries no status argument — so the only observable
// effect of a full reconciliation pass is on root(), never anything else the
// fake tracks. This is a compile-time property (the interface has one write
// method) exercised here so a future widening of the interface would have to
// touch this test.
func TestReconciler_NeverTouchesAnythingButRootIncidentID(t *testing.T) {
	store := newFakeStore()
	store.addIncident("t1", "inc-x", "asset-x")
	store.addIncident("t1", "inc-db", "asset-db")
	graph := newFakeGraph()
	graph.set("t1", "asset-x", []domain.TraversalNode{{CfgID: "asset-db", Depth: 1}})
	graph.set("t1", "asset-db", nil)

	r := NewReconciler(store, graph, NopMetrics{}, nil, Config{})
	r.RunOnce(context.Background())

	for _, call := range store.setCalls {
		if call.incidentID == "" {
			t.Errorf("a SetRootIncidentID call named no incident")
		}
	}
	// The only two incidents ever created stay exactly the two incidents that
	// exist: grouping never creates, deletes, or resolves anything.
	if got := len(store.byTenant["t1"]); got != 2 {
		t.Errorf("incident count after reconciliation = %d, want 2 (grouping must never create or remove one)", got)
	}
}

// TestReconciler_EmptyTenantSetIsANoOp is the vacuity guard: no open
// alert-incidents anywhere means no tenants are even discovered, and no
// writes happen.
func TestReconciler_EmptyTenantSetIsANoOp(t *testing.T) {
	store := newFakeStore()
	graph := newFakeGraph()
	r := NewReconciler(store, graph, NopMetrics{}, nil, Config{})
	r.RunOnce(context.Background())
	if got := store.writeCount(); got != 0 {
		t.Errorf("writes with no open incidents anywhere = %d, want 0", got)
	}
}
