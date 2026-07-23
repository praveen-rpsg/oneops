package authority

import (
	"context"
	"errors"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// respGraph extends the M3.1 fakeGraph with responsibility ownership, so it
// satisfies both domain.AuthorityGraph and domain.ResponsibilityGraph.
type respGraph struct {
	*fakeGraph
	resp map[string][]string
}

func rgraph() *respGraph { return &respGraph{fakeGraph: g(), resp: map[string][]string{}} }

func (r *respGraph) owns(id string, responsibilities ...string) *respGraph {
	r.node(id)
	r.resp[id] = append(r.resp[id], responsibilities...)
	return r
}

func (r *respGraph) Responsibilities(_ context.Context, id string) ([]string, error) {
	return r.resp[id], nil
}

func resolveR(t *testing.T, f *respGraph, id string) domain.AuthorityResult {
	t.Helper()
	res, err := NewResolver(f).ResolveAuthority(context.Background(), id)
	if err != nil {
		t.Fatalf("resolve %s: %v", id, err)
	}
	return res
}

// Full transfer: New owns every responsibility of Old → Old is Historical.
func TestFullResponsibilityTransfer(t *testing.T) {
	f := rgraph()
	f.base("New")
	f.owns("New", "R1", "R2").owns("Old", "R1", "R2")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	assertState(t, resolveR(t, f, "Old"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
}

// Partial transfer: New misses R2 → Old remains Active (replacement_incomplete).
func TestPartialResponsibilityTransfer(t *testing.T) {
	f := rgraph()
	f.base("New")
	f.owns("New", "R1").owns("Old", "R1", "R2")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	r := resolveR(t, f, "Old")
	assertState(t, r, domain.AuthorityStateActive, domain.ReasonReplacementIncomplete)
	if len(r.Evidence.MissingResponsibilities) != 1 || r.Evidence.MissingResponsibilities[0] != "R2" {
		t.Errorf("missing = %v, want [R2]", r.Evidence.MissingResponsibilities)
	}
}

// Empty responsibility set on Old: trivially complete → Historical.
func TestEmptyResponsibilitySet(t *testing.T) {
	f := rgraph()
	f.base("New")
	f.node("Old") // Old owns nothing
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	assertState(t, resolveR(t, f, "Old"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
}

// Multiple replacements collectively cover Old's responsibilities → Historical.
func TestMultipleReplacements(t *testing.T) {
	f := rgraph()
	f.base("N1", "N2")
	f.owns("N1", "R1").owns("N2", "R2").owns("Old", "R1", "R2")
	f.edge("N1", "Old", domain.EdgeKindSupersedes)
	f.edge("N2", "Old", domain.EdgeKindSupersedes)
	assertState(t, resolveR(t, f, "Old"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
}

// Duplicate responsibility ids → structured error.
func TestDuplicateResponsibilityIDs(t *testing.T) {
	f := rgraph()
	f.base("New")
	f.owns("New", "R1").owns("Old", "R1", "R1")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	_, err := NewResolver(f).ResolveAuthority(context.Background(), "Old")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Kind != "duplicate_responsibility" {
		t.Fatalf("expected duplicate_responsibility, got %v", err)
	}
}

func TestEvaluateResponsibilitiesAPI(t *testing.T) {
	f := rgraph().owns("X", "Rb", "Ra") // out of order on purpose
	e := NewResponsibilityEvaluator(f)
	res, err := e.EvaluateResponsibilities(context.Background(), "X")
	if err != nil {
		t.Fatal(err)
	}
	// sorted, deterministic
	if len(res.Responsibilities) != 2 || res.Responsibilities[0] != "Ra" || res.Responsibilities[1] != "Rb" {
		t.Fatalf("responsibilities = %v, want [Ra Rb]", res.Responsibilities)
	}
	if _, err := e.EvaluateResponsibilities(context.Background(), "missing"); err == nil {
		t.Error("expected missing-object error")
	}
}

func TestEvaluateReplacementAPI(t *testing.T) {
	f := rgraph().owns("Old", "R1", "R2", "R3").owns("New", "R1", "R2")
	e := NewResponsibilityEvaluator(f)

	res, err := e.EvaluateReplacement(context.Background(), "Old", "New")
	if err != nil {
		t.Fatal(err)
	}
	if res.Complete {
		t.Error("expected incomplete")
	}
	if len(res.Missing) != 1 || res.Missing[0] != "R3" {
		t.Errorf("missing = %v, want [R3]", res.Missing)
	}
	if len(res.Transferred) != 2 {
		t.Errorf("transferred = %v, want 2", res.Transferred)
	}

	// Empty target and broken mapping are structured errors.
	if _, err := e.EvaluateReplacement(context.Background(), "Old", ""); err == nil {
		t.Error("expected empty_replacement_target error")
	}
	if _, err := e.EvaluateReplacement(context.Background(), "Old", "nope"); err == nil {
		t.Error("expected broken_replacement error")
	}
}

func TestEvaluateResponsibilityBatch(t *testing.T) {
	f := rgraph().owns("A", "R1").owns("B", "R2", "R3")
	e := NewResponsibilityEvaluator(f)
	out, err := e.EvaluateBatch(context.Background(), []string{"A", "B"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].CfgID != "A" || len(out[1].Responsibilities) != 2 {
		t.Fatalf("batch = %+v", out)
	}
}

// M3.2 behavior is preserved: with active dependency, active-dependency wins
// before responsibility evaluation is even reached.
func TestActiveDependencyStillWinsOverResponsibility(t *testing.T) {
	f := rgraph()
	f.base("B")
	f.owns("New", "R1").owns("Old", "R1", "R2") // replacement incomplete...
	f.edge("B", "Old", domain.EdgeKindDepends)  // ...but B (active) still depends on Old
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	assertState(t, resolveR(t, f, "Old"), domain.AuthorityStateActive, domain.ReasonSupersededActiveDependency)
}
