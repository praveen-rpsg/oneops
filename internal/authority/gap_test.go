package authority

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// gapGraph extends the M3.1 fakeGraph with responsibility ownership, artifact
// citations, and operational coverage, so it satisfies domain.AuthorityGraph,
// domain.ResponsibilityGraph, domain.CitationGraph, and domain.CoverageGraph at
// once. This lets a single fake exercise the full M3.2 → M3.3 → M3.4 → M3.5
// authority precedence.
type gapGraph struct {
	*fakeGraph
	resp  map[string][]string
	cites map[string][]string
	cover map[string][]string
}

func gapg() *gapGraph {
	return &gapGraph{fakeGraph: g(), resp: map[string][]string{}, cites: map[string][]string{}, cover: map[string][]string{}}
}

func (c *gapGraph) owns(id string, rs ...string) *gapGraph {
	c.node(id)
	c.resp[id] = append(c.resp[id], rs...)
	return c
}

func (c *gapGraph) cite(id string, targets ...string) *gapGraph {
	c.node(id)
	c.cites[id] = append(c.cites[id], targets...)
	return c
}

// covers declares the operational-coverage ids provided by id (M3.5 metadata).
func (c *gapGraph) covers(id string, caps ...string) *gapGraph {
	c.node(id)
	c.cover[id] = append(c.cover[id], caps...)
	return c
}

func (c *gapGraph) Responsibilities(_ context.Context, id string) ([]string, error) {
	return c.resp[id], nil
}
func (c *gapGraph) Citations(_ context.Context, id string) ([]string, error) {
	return c.cites[id], nil
}
func (c *gapGraph) Coverage(_ context.Context, id string) ([]string, error) {
	return c.cover[id], nil
}

func resolveG(t *testing.T, f *gapGraph, id string) domain.AuthorityResult {
	t.Helper()
	res, err := NewResolver(f).ResolveAuthority(context.Background(), id)
	if err != nil {
		t.Fatalf("resolve %s: %v", id, err)
	}
	return res
}

func gapEval(t *testing.T, f *gapGraph) *GapEvaluator {
	t.Helper()
	e := NewResolver(f).gaps
	if e == nil {
		t.Fatal("gap evaluator not wired: data does not satisfy domain.CoverageGraph")
	}
	return e
}

// --- required cases ----------------------------------------------------------

// no operational gap: an ACTIVE config (baseline New) provides all of Old's
// coverage → removing Old leaves nothing unowned → Old is Historical.
func TestNoOperationalGap(t *testing.T) {
	f := gapg()
	f.base("New")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.covers("Old", "C1").covers("New", "C1")
	assertState(t, resolveG(t, f, "Old"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
}

// operational gap detected: Old provides {C1,C2}; the only ACTIVE config covers
// C1 → C2 would be left unowned → Old remains Active with reason operational_gap
// and evidence.
func TestOperationalGapDetected(t *testing.T) {
	f := gapg()
	f.base("New")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.covers("Old", "C1", "C2").covers("New", "C1")
	r := resolveG(t, f, "Old")
	assertState(t, r, domain.AuthorityStateActive, domain.ReasonOperationalGap)
	if !reflect.DeepEqual(r.Evidence.UncoveredCapabilities, []string{"C2"}) {
		t.Errorf("uncovered = %v, want [C2]", r.Evidence.UncoveredCapabilities)
	}
	// structured evidence: the supersession is still recorded.
	if len(r.Evidence.SupersededBy) != 1 || r.Evidence.SupersededBy[0] != "New" {
		t.Errorf("superseded_by = %v, want [New]", r.Evidence.SupersededBy)
	}
}

// duplicate coverage IDs on the evaluated object → structured error.
func TestDuplicateCoverageIDs(t *testing.T) {
	f := gapg()
	f.base("New")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.covers("Old", "C1", "C1")
	_, err := NewResolver(f).ResolveAuthority(context.Background(), "Old")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Kind != "duplicate_coverage" {
		t.Fatalf("expected duplicate_coverage ValidationError, got %v", err)
	}
}

// malformed metadata (empty/whitespace coverage segment) → structured error.
func TestMalformedCoverageMetadata(t *testing.T) {
	f := gapg()
	f.base("New")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.covers("Old", "C1", "   ")
	_, err := NewResolver(f).ResolveAuthority(context.Background(), "Old")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Kind != "malformed_coverage_metadata" {
		t.Fatalf("expected malformed_coverage_metadata ValidationError, got %v", err)
	}
}

// replacement completeness: EvaluateReplacement compares old vs new coverage.
func TestGapEvaluateReplacementAPI(t *testing.T) {
	f := gapg()
	f.covers("Old", "C1", "C2", "C3").covers("New", "C1", "C2")
	e := gapEval(t, f)

	res, err := e.EvaluateReplacement(context.Background(), "Old", "New")
	if err != nil {
		t.Fatal(err)
	}
	if res.Complete {
		t.Error("expected incomplete: New misses C3")
	}
	if !reflect.DeepEqual(res.Missing, []string{"C3"}) {
		t.Errorf("missing = %v, want [C3]", res.Missing)
	}
	if len(res.Covered) != 2 {
		t.Errorf("covered = %v, want 2", res.Covered)
	}

	// empty target and broken mapping are structured errors.
	if _, err := e.EvaluateReplacement(context.Background(), "Old", ""); err == nil {
		t.Error("expected empty_replacement_target error")
	}
	if _, err := e.EvaluateReplacement(context.Background(), "Old", "nope"); err == nil {
		t.Error("expected broken_replacement error")
	}
}

// A replacement that provides all coverage is complete.
func TestGapReplacementComplete(t *testing.T) {
	f := gapg()
	f.covers("Old", "C1").covers("New", "C1", "C2")
	res, err := gapEval(t, f).EvaluateReplacement(context.Background(), "Old", "New")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Complete || len(res.Missing) != 0 {
		t.Fatalf("expected complete with no missing, got %+v", res)
	}
}

// batch evaluation shares one active-set computation and preserves input order.
func TestGapBatch(t *testing.T) {
	f := gapg()
	f.base("Prov")
	f.covers("Prov", "C1")       // active provider covers C1 only
	f.covers("Old1", "C1")       // fully covered → no gap
	f.covers("Old2", "C1", "C9") // C9 uncovered → gap
	out, err := gapEval(t, f).EvaluateBatch(context.Background(), []string{"Old1", "Old2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].CfgID != "Old1" || out[1].CfgID != "Old2" {
		t.Fatalf("batch order/shape = %+v", out)
	}
	if out[0].HasGap {
		t.Errorf("Old1 should have no gap: %+v", out[0])
	}
	if !out[1].HasGap || !reflect.DeepEqual(out[1].UncoveredCapabilities, []string{"C9"}) {
		t.Errorf("Old2 gap = %+v, want [C9]", out[1])
	}
}

// deterministic ordering: multiple uncovered ids are sorted and stable.
func TestGapDeterministicOrdering(t *testing.T) {
	f := gapg()
	f.base("Prov")
	f.covers("Prov", "C5")
	f.covers("Old", "Cz", "Ca", "Cm", "C5") // C5 covered; Ca,Cm,Cz uncovered
	e := gapEval(t, f)
	want := []string{"Ca", "Cm", "Cz"}
	first, err := e.EvaluateGap(context.Background(), "Old")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.UncoveredCapabilities, want) {
		t.Fatalf("uncovered = %v, want %v", first.UncoveredCapabilities, want)
	}
	for i := 0; i < 5; i++ {
		got, err := e.EvaluateGap(context.Background(), "Old")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.UncoveredCapabilities, want) {
			t.Fatalf("gap ordering not deterministic: %v", got.UncoveredCapabilities)
		}
	}
}

// EvaluateGap API surface, incl. missing object.
func TestEvaluateGapAPI(t *testing.T) {
	f := gapg()
	f.base("Prov")
	f.covers("Prov", "C1")
	f.covers("Old", "C1", "C2")
	e := gapEval(t, f)

	res, err := e.EvaluateGap(context.Background(), "Old")
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasGap || !reflect.DeepEqual(res.UncoveredCapabilities, []string{"C2"}) {
		t.Fatalf("EvaluateGap = %+v, want gap [C2]", res)
	}
	if _, err := e.EvaluateGap(context.Background(), "missing"); err == nil {
		t.Error("expected missing-object error")
	}
}

// An object that declares no coverage can never create a gap.
func TestNoCoverageNoGap(t *testing.T) {
	f := gapg()
	f.base("New")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	assertState(t, resolveG(t, f, "Old"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
}

// --- precedence (M3.2 / M3.3 / M3.4 win before the gap clause) ---------------

// Active dependency (M3.2) wins over gap.
func TestActiveDependencyWinsOverGap(t *testing.T) {
	f := gapg()
	f.base("B")
	f.edge("B", "Old", domain.EdgeKindDepends) // active dependency
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.covers("Old", "C1", "C2") // would be a gap (C2 unowned)
	assertState(t, resolveG(t, f, "Old"), domain.AuthorityStateActive, domain.ReasonSupersededActiveDependency)
}

// Incomplete responsibility transfer (M3.3) wins over gap.
func TestResponsibilityIncompleteWinsOverGap(t *testing.T) {
	f := gapg()
	f.base("B", "New")
	f.owns("Old", "R1") // New owns nothing → replacement incomplete
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.covers("Old", "C1", "C2") // would also be a gap
	assertState(t, resolveG(t, f, "Old"), domain.AuthorityStateActive, domain.ReasonReplacementIncomplete)
}

// Active artifact citation (M3.4) wins over gap.
func TestCitationWinsOverGap(t *testing.T) {
	f := gapg()
	f.base("B", "New")
	f.cite("B", "Old") // active citation
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.covers("Old", "C1", "C2") // would also be a gap
	assertState(t, resolveG(t, f, "Old"), domain.AuthorityStateActive, domain.ReasonActiveArtifactCitation)
}

// M3.1 plain supersession (no evaluators triggered) is unchanged: superseded with
// no coverage, dependency, responsibility, or citation → Historical.
func TestGapDoesNotDisturbPlainSupersession(t *testing.T) {
	f := gapg()
	f.base("New")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	assertState(t, resolveG(t, f, "New"), domain.AuthorityStateActive, domain.ReasonBaselineMember)
	assertState(t, resolveG(t, f, "Old"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
}
