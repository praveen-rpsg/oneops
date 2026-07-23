package authority

import (
	"context"
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// F1 core: a superseded config still required by an ACTIVE config stays ACTIVE.
// Baseline B depends on X; Y supersedes X. X must resolve ACTIVE (not Historical).
func TestSupersededButActivelyReferenced(t *testing.T) {
	f := g().base("B").edge("B", "X", domain.EdgeKindDepends).edge("Y", "X", domain.EdgeKindSupersedes)
	r := resolve(t, f, "X")
	assertState(t, r, domain.AuthorityStateActive, domain.ReasonSupersededActiveDependency)
	if len(r.Evidence.SupersededBy) != 1 || r.Evidence.SupersededBy[0] != "Y" {
		t.Errorf("superseded_by = %v, want [Y]", r.Evidence.SupersededBy)
	}
	if len(r.Evidence.ActiveDependents) != 1 || r.Evidence.ActiveDependents[0] != "B" {
		t.Errorf("active_dependents = %v, want [B]", r.Evidence.ActiveDependents)
	}
}

// Superseded and NOT actively referenced -> Historical (unchanged from M3.1).
func TestSupersededAndUnreferenced(t *testing.T) {
	f := g().base("B").edge("Y", "Old", domain.EdgeKindSupersedes)
	assertState(t, resolve(t, f, "Old"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
}

// A non-Active dependent does NOT keep a superseded object Active.
func TestSupersededReferencedByInactive(t *testing.T) {
	// Z depends on Old, but Z is not reachable from any baseline (inactive).
	f := g().base("B").edge("Y", "Old", domain.EdgeKindSupersedes).edge("Z", "Old", domain.EdgeKindDepends)
	assertState(t, resolve(t, f, "Old"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
}

func TestMultipleActiveBaselinesReference(t *testing.T) {
	f := g().base("B1", "B2").edge("B1", "X", domain.EdgeKindDepends).edge("W", "X", domain.EdgeKindSupersedes)
	r := resolve(t, f, "X")
	assertState(t, r, domain.AuthorityStateActive, domain.ReasonSupersededActiveDependency)
	if len(r.Evidence.ActiveDependents) != 1 || r.Evidence.ActiveDependents[0] != "B1" {
		t.Errorf("active_dependents = %v, want [B1]", r.Evidence.ActiveDependents)
	}
}

// Dependency cycle among active nodes plus a superseded node: resolution
// terminates and the superseded-but-required node stays Active.
func TestActiveDependencyCycle(t *testing.T) {
	f := g().base("B").
		edge("B", "X", domain.EdgeKindDepends).
		edge("X", "Y", domain.EdgeKindDepends).
		edge("Y", "X", domain.EdgeKindDepends). // cycle X<->Y
		edge("W", "Y", domain.EdgeKindSupersedes)
	r := resolve(t, f, "Y")
	assertState(t, r, domain.AuthorityStateActive, domain.ReasonSupersededActiveDependency)
	if len(r.Evidence.ActiveDependents) == 0 {
		t.Error("expected active dependents evidence")
	}
}

// Determinism: multiple active dependents come back sorted, stably.
func TestActiveDependentsDeterministic(t *testing.T) {
	f := g().base("Ba", "Bb", "Bc").
		edge("Bc", "X", domain.EdgeKindDepends).
		edge("Ba", "X", domain.EdgeKindDepends).
		edge("Bb", "X", domain.EdgeKindExtends).
		edge("Y", "X", domain.EdgeKindSupersedes)
	r := resolve(t, f, "X")
	got := r.Evidence.ActiveDependents
	want := []string{"Ba", "Bb", "Bc"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("active_dependents = %v, want %v (sorted)", got, want)
	}
}

func TestEvaluateActiveDependenciesAPI(t *testing.T) {
	f := g().base("B").edge("B", "X", domain.EdgeKindDepends).edge("Y", "X", domain.EdgeKindSupersedes).node("Free")
	e := NewActiveDependencyEvaluator(NewResolver(f))
	ctx := context.Background()

	x, err := e.EvaluateActiveDependencies(ctx, "X")
	if err != nil {
		t.Fatal(err)
	}
	if !x.HasActiveDependency || len(x.ActiveDependents) != 1 || x.ActiveDependents[0] != "B" {
		t.Fatalf("X: %+v", x)
	}
	free, err := e.EvaluateActiveDependencies(ctx, "Free")
	if err != nil {
		t.Fatal(err)
	}
	if free.HasActiveDependency {
		t.Errorf("Free should have no active dependency: %+v", free)
	}
}

func TestEvaluateBatch(t *testing.T) {
	f := g().base("B").edge("B", "X", domain.EdgeKindDepends).edge("Y", "Old", domain.EdgeKindSupersedes)
	e := NewActiveDependencyEvaluator(NewResolver(f))
	ids := []string{"X", "Old", "B"}
	out, err := e.EvaluateBatch(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatalf("batch len = %d", len(out))
	}
	for i, id := range ids {
		if out[i].CfgID != id {
			t.Fatalf("batch order broken at %d: %s", i, out[i].CfgID)
		}
	}
	if !out[0].HasActiveDependency { // X required by B
		t.Errorf("X should have active dependency")
	}
	if out[1].HasActiveDependency { // Old unreferenced
		t.Errorf("Old should not have active dependency")
	}
}

func TestEvaluateMissingNode(t *testing.T) {
	e := NewActiveDependencyEvaluator(NewResolver(g().base("B")))
	_, err := e.EvaluateActiveDependencies(context.Background(), "nope")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Kind != "missing_object" {
		t.Fatalf("expected missing_object ValidationError, got %v", err)
	}
	_, err = e.EvaluateBatch(context.Background(), []string{"nope"})
	if !errors.As(err, &ve) || ve.Kind != "missing_object" {
		t.Fatalf("batch expected missing_object ValidationError, got %v", err)
	}
}

// Regression guard: M3.1 supersession behavior (no active dependency) is preserved.
func TestM31SupersessionStillHistorical(t *testing.T) {
	f := g().base("New").edge("New", "Old", domain.EdgeKindSupersedes)
	assertState(t, resolve(t, f, "Old"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
}
