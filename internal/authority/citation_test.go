package authority

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/rpsg/oneops/internal/domain"
)

// citeGraph extends the M3.1 fakeGraph with responsibility ownership and artifact
// citations, so it satisfies domain.AuthorityGraph, domain.ResponsibilityGraph,
// and domain.CitationGraph at once. This lets a single fake exercise the full
// M3.2 → M3.3 → M3.4 authority precedence.
type citeGraph struct {
	*fakeGraph
	resp  map[string][]string
	cites map[string][]string
}

func cgraph() *citeGraph {
	return &citeGraph{fakeGraph: g(), resp: map[string][]string{}, cites: map[string][]string{}}
}

// owns declares responsibilities owned by id (M3.3 metadata).
func (c *citeGraph) owns(id string, rs ...string) *citeGraph {
	c.node(id)
	c.resp[id] = append(c.resp[id], rs...)
	return c
}

// cite declares that artifact id cites each target (M3.4 metadata). Only the
// citing artifact is registered as a node; targets are registered explicitly so
// tests can also model unknown references.
func (c *citeGraph) cite(id string, targets ...string) *citeGraph {
	c.node(id)
	c.cites[id] = append(c.cites[id], targets...)
	return c
}

func (c *citeGraph) Responsibilities(_ context.Context, id string) ([]string, error) {
	return c.resp[id], nil
}

func (c *citeGraph) Citations(_ context.Context, id string) ([]string, error) {
	return c.cites[id], nil
}

func resolveC(t *testing.T, f *citeGraph, id string) domain.AuthorityResult {
	t.Helper()
	res, err := NewResolver(f).ResolveAuthority(context.Background(), id)
	if err != nil {
		t.Fatalf("resolve %s: %v", id, err)
	}
	return res
}

func citeEval(t *testing.T, f *citeGraph) *ArtifactCitationEvaluator {
	t.Helper()
	e := NewResolver(f).citations
	if e == nil {
		t.Fatal("citation evaluator not wired: data does not satisfy domain.CitationGraph")
	}
	return e
}

// --- required cases ----------------------------------------------------------

// active citations present: an ACTIVE artifact (baseline B) cites the superseded
// Old → Old remains Active with reason active_artifact_citation and evidence.
func TestActiveCitationKeepsSupersededActive(t *testing.T) {
	f := cgraph()
	f.base("B", "New")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.cite("B", "Old")
	r := resolveC(t, f, "Old")
	assertState(t, r, domain.AuthorityStateActive, domain.ReasonActiveArtifactCitation)
	if len(r.Evidence.ActiveArtifactCitations) != 1 || r.Evidence.ActiveArtifactCitations[0] != "B" {
		t.Errorf("citing artifacts = %v, want [B]", r.Evidence.ActiveArtifactCitations)
	}
	// structured evidence: the supersession is still recorded.
	if len(r.Evidence.SupersededBy) != 1 || r.Evidence.SupersededBy[0] != "New" {
		t.Errorf("superseded_by = %v, want [New]", r.Evidence.SupersededBy)
	}
}

// no citations: superseded Old with a complete replacement and no citer → Historical.
func TestNoCitationSupersededHistorical(t *testing.T) {
	f := cgraph()
	f.base("New")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	assertState(t, resolveC(t, f, "Old"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
}

// inactive citations ignored: a non-active artifact C cites Old → Old still
// resolves Historical (only ACTIVE artifacts count).
func TestInactiveCitationIgnored(t *testing.T) {
	f := cgraph()
	f.base("New")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.cite("C", "Old") // C exists but is neither baseline nor reachable → not active
	assertState(t, resolveC(t, f, "Old"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
}

// duplicate citation IDs in an active artifact → structured error, no silent failure.
func TestDuplicateCitationIDs(t *testing.T) {
	f := cgraph()
	f.base("B", "New")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.cite("B", "Old", "Old")
	_, err := NewResolver(f).ResolveAuthority(context.Background(), "Old")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Kind != "duplicate_citation" {
		t.Fatalf("expected duplicate_citation ValidationError, got %v", err)
	}
}

// malformed metadata (empty/whitespace citation segment) → structured error.
func TestMalformedCitationMetadata(t *testing.T) {
	f := cgraph()
	f.base("B", "New")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.cite("B", "Old", "   ") // empty segment after trim
	_, err := NewResolver(f).ResolveAuthority(context.Background(), "Old")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Kind != "malformed_citation_metadata" {
		t.Fatalf("expected malformed_citation_metadata ValidationError, got %v", err)
	}
}

// unknown artifact reference: an active artifact cites a non-existent object →
// structured error.
func TestUnknownArtifactReference(t *testing.T) {
	f := cgraph()
	f.base("B")
	f.node("Old")
	f.cite("B", "Ghost") // Ghost is never registered as a node
	_, err := citeEval(t, f).EvaluateCitations(context.Background(), "Old")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Kind != "unknown_artifact_reference" {
		t.Fatalf("expected unknown_artifact_reference ValidationError, got %v", err)
	}
}

// replacement complete + citations: responsibilities are fully transferred (M3.3
// passes) but an ACTIVE artifact still cites Old → Old stays Active via the
// citation clause, proving M3.4 fires after M3.3.
func TestReplacementCompleteButActivelyCited(t *testing.T) {
	f := cgraph()
	f.base("B", "New")
	f.owns("New", "R1").owns("Old", "R1") // replacement complete
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.cite("B", "Old")
	assertState(t, resolveC(t, f, "Old"), domain.AuthorityStateActive, domain.ReasonActiveArtifactCitation)
}

// batch evaluation shares one active-set computation and preserves input order.
func TestCitationBatch(t *testing.T) {
	f := cgraph()
	f.base("B")
	f.node("Old1", "Old2")
	f.cite("B", "Old1")
	out, err := citeEval(t, f).EvaluateBatch(context.Background(), []string{"Old1", "Old2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].CfgID != "Old1" || out[1].CfgID != "Old2" {
		t.Fatalf("batch order/shape = %+v", out)
	}
	if !out[0].HasActiveCitation || out[1].HasActiveCitation {
		t.Fatalf("batch results = %+v, want Old1 cited, Old2 not", out)
	}
}

// deterministic ordering: many active artifacts cite Old → CitingArtifacts is
// sorted and stable across repeated evaluation.
func TestCitationDeterministicOrdering(t *testing.T) {
	f := cgraph()
	f.base("Z", "A", "M")
	f.node("Old")
	f.cite("Z", "Old").cite("A", "Old").cite("M", "Old")
	e := citeEval(t, f)
	want := []string{"A", "M", "Z"}
	first, err := e.EvaluateCitations(context.Background(), "Old")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.CitingArtifacts, want) {
		t.Fatalf("citing = %v, want %v", first.CitingArtifacts, want)
	}
	for i := 0; i < 5; i++ {
		got, err := e.EvaluateCitations(context.Background(), "Old")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.CitingArtifacts, want) {
			t.Fatalf("citation ordering not deterministic: %v", got.CitingArtifacts)
		}
	}
}

// --- precedence (M3.3 behaviour unchanged outside citation evaluation) --------

// Active dependency (M3.2) wins over citation: reason stays superseded_active_dependency.
func TestActiveDependencyWinsOverCitation(t *testing.T) {
	f := cgraph()
	f.base("B")
	f.edge("B", "Old", domain.EdgeKindDepends) // active dependency
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.cite("B", "Old") // also cited
	assertState(t, resolveC(t, f, "Old"), domain.AuthorityStateActive, domain.ReasonSupersededActiveDependency)
}

// Incomplete replacement (M3.3) wins over citation: reason stays replacement_incomplete.
func TestResponsibilityIncompleteWinsOverCitation(t *testing.T) {
	f := cgraph()
	f.base("B", "New")
	f.owns("Old", "R1") // New owns nothing → replacement incomplete
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.cite("B", "Old")
	assertState(t, resolveC(t, f, "Old"), domain.AuthorityStateActive, domain.ReasonReplacementIncomplete)
}

// --- evaluator API surface ----------------------------------------------------

func TestEvaluateCitationsAPI(t *testing.T) {
	f := cgraph()
	f.base("B")
	f.node("Old")
	f.cite("B", "Old")
	e := citeEval(t, f)

	res, err := e.EvaluateCitations(context.Background(), "Old")
	if err != nil {
		t.Fatal(err)
	}
	if !res.HasActiveCitation || len(res.CitingArtifacts) != 1 || res.CitingArtifacts[0] != "B" {
		t.Fatalf("EvaluateCitations = %+v", res)
	}
	if _, err := e.EvaluateCitations(context.Background(), "missing"); err == nil {
		t.Error("expected missing-object error")
	}
}

func TestCitationEvaluateReplacementAPI(t *testing.T) {
	f := cgraph()
	f.base("B", "New")
	f.node("Old")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.cite("B", "Old")
	e := citeEval(t, f)

	res, err := e.EvaluateReplacement(context.Background(), "Old", "New")
	if err != nil {
		t.Fatal(err)
	}
	if res.Cleared {
		t.Error("expected not cleared: B still cites Old")
	}
	if len(res.Remaining) != 1 || res.Remaining[0] != "B" {
		t.Errorf("remaining = %v, want [B]", res.Remaining)
	}

	// broken replacement mapping is a structured error.
	if _, err := e.EvaluateReplacement(context.Background(), "Old", ""); err == nil {
		t.Error("expected empty_replacement_target error")
	}
	if _, err := e.EvaluateReplacement(context.Background(), "Old", "nope"); err == nil {
		t.Error("expected broken_replacement error")
	}
}

// A replacement with no active citer is cleared.
func TestCitationReplacementCleared(t *testing.T) {
	f := cgraph()
	f.base("New")
	f.node("Old")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	res, err := citeEval(t, f).EvaluateReplacement(context.Background(), "Old", "New")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Cleared || len(res.Remaining) != 0 {
		t.Fatalf("expected cleared with no remaining, got %+v", res)
	}
}

// A config citing itself does not keep itself Active.
func TestSelfCitationDoesNotSustain(t *testing.T) {
	f := cgraph()
	f.base("New")
	f.edge("New", "Old", domain.EdgeKindSupersedes)
	f.cite("Old", "Old") // Old cites itself
	assertState(t, resolveC(t, f, "Old"), domain.AuthorityStateHistorical, domain.ReasonSuperseded)
}
