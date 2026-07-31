package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/kg/extract/gopkg"
	"github.com/rpsg/oneops/internal/kg/graph"
	"github.com/rpsg/oneops/internal/kg/model"
)

const repoRoot = "../../.."

// fake is an extractor under the test's control. A real one cannot be made to
// fail, emit an invalid graph, or return its results out of order, so the
// pipeline's own guarantees could not otherwise be exercised.
type fake struct {
	id    string
	nodes []graph.Node
	edges []graph.Edge
	err   error
}

func (f fake) ID() string { return f.id }
func (f fake) Extract(context.Context, string) ([]graph.Node, []graph.Edge, error) {
	return f.nodes, f.edges, f.err
}

func node(id string) graph.Node {
	return graph.Node{
		ID: id, Kind: "package",
		Evidence:   []graph.Evidence{{Source: "internal/kg/pipeline/pipeline.go", Rule: "T.node"}},
		Origin:     model.OriginDerived,
		Confidence: model.ConfidenceCertain,
	}
}

func edge(from, to string) graph.Edge {
	return graph.Edge{
		From: from, To: to, Kind: "imports",
		Evidence:   []graph.Evidence{{Source: "internal/kg/pipeline/pipeline.go", Rule: "T.edge"}},
		Origin:     model.OriginDerived,
		Confidence: model.ConfidenceCertain,
	}
}

// ---------------------------------------------------------------- positive

func TestBuildProducesAValidGraph(t *testing.T) {
	g, err := Build(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// Build validates before returning; re-asserted so a change that removed
	// the call would fail here rather than downstream.
	if verr := g.Validate(); verr != nil {
		t.Fatalf("the returned graph does not satisfy §II: %v", verr)
	}
	if g.SchemaVersion != graph.SchemaVersion {
		t.Errorf("schema version = %d, want %d", g.SchemaVersion, graph.SchemaVersion)
	}
	if len(g.Nodes) == 0 || len(g.Edges) == 0 {
		t.Fatalf("empty graph: %d nodes, %d edges", len(g.Nodes), len(g.Edges))
	}
	t.Logf("nodes=%d edges=%d commit=%s", len(g.Nodes), len(g.Edges), g.Commit)
}

// Freshness is Graph.Commit and nothing else (Amendment A3 §C3).
func TestCommitMatchesGitHead(t *testing.T) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	want := strings.TrimSpace(string(out))

	g, err := Build(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if g.Commit != want {
		t.Errorf("commit = %q, want %q", g.Commit, want)
	}
	if len(g.Commit) != 40 {
		t.Errorf("commit %q is not a full 40-character object name", g.Commit)
	}
}

// Two builds over an unchanged tree must agree, structurally and byte for byte.
func TestBuildIsDeterministic(t *testing.T) {
	first, err := Build(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	wantJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for i := 0; i < 3; i++ {
		again, berr := Build(context.Background(), repoRoot)
		if berr != nil {
			t.Fatalf("build %d: %v", i, berr)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("build %d differs structurally", i)
		}
		gotJSON, merr := json.Marshal(again)
		if merr != nil {
			t.Fatalf("marshal %d: %v", i, merr)
		}
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("build %d differs byte for byte", i)
		}
	}
}

// Concatenating two sorted runs does not give a sorted whole. The pipeline's
// normalise stage is what re-establishes canonical order over the merge, and
// without it graph.Validate would reject every multi-extractor build.
func TestMergedOutputIsNormalised(t *testing.T) {
	a := fake{id: "TA", nodes: []graph.Node{node("package:m"), node("package:a")},
		edges: []graph.Edge{edge("package:m", "package:a")}}
	b := fake{id: "TB", nodes: []graph.Node{node("package:z"), node("package:b")},
		edges: []graph.Edge{edge("package:b", "package:z"), edge("package:a", "package:b")}}

	g, err := BuildWith(context.Background(), repoRoot, []Extractor{a, b})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	wantNodes := []string{"package:a", "package:b", "package:m", "package:z"}
	got := make([]string, len(g.Nodes))
	for i, n := range g.Nodes {
		got[i] = n.ID
	}
	if !reflect.DeepEqual(got, wantNodes) {
		t.Errorf("nodes not normalised: got %v, want %v", got, wantNodes)
	}
	wantEdges := [][2]string{
		{"package:a", "package:b"}, {"package:b", "package:z"}, {"package:m", "package:a"},
	}
	for i, w := range wantEdges {
		if g.Edges[i].From != w[0] || g.Edges[i].To != w[1] {
			t.Errorf("edge %d = %s->%s, want %s->%s", i, g.Edges[i].From, g.Edges[i].To, w[0], w[1])
		}
	}
}

func TestDefaultRegistryIsE1(t *testing.T) {
	reg := Default()
	if len(reg) != 1 {
		t.Fatalf("registry has %d extractors, want 1", len(reg))
	}
	if reg[0].ID() != gopkg.ExtractorID {
		t.Errorf("registered %q, want %q", reg[0].ID(), gopkg.ExtractorID)
	}
}

// ---------------------------------------------------------------- negative

// Validation is mandatory: a graph that violates §II must never be returned,
// so nothing downstream has to ask whether it was checked.
func TestValidationIsMandatory(t *testing.T) {
	cases := []struct {
		name string
		ex   fake
	}{
		{"dangling edge", fake{id: "TX", nodes: []graph.Node{node("package:a")},
			edges: []graph.Edge{edge("package:a", "package:absent")}}},
		{"duplicate node id", fake{id: "TX", nodes: []graph.Node{node("package:a"), node("package:a")}}},
		{"node without evidence", fake{id: "TX", nodes: []graph.Node{{
			ID: "package:a", Kind: "package",
			Origin: model.OriginDerived, Confidence: model.ConfidenceCertain}}}},
		{"unknown origin", fake{id: "TX", nodes: []graph.Node{{
			ID: "package:a", Kind: "package",
			Evidence:   []graph.Evidence{{Source: "go.mod", Rule: "T.node"}},
			Origin:     "guessed",
			Confidence: model.ConfidenceCertain}}}},
		{"absolute evidence source (A1)", fake{id: "TX", nodes: []graph.Node{{
			ID: "package:a", Kind: "package",
			Evidence:   []graph.Evidence{{Source: "/Users/someone/oneops/go.mod", Rule: "T.node"}},
			Origin:     model.OriginDerived,
			Confidence: model.ConfidenceCertain}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, err := BuildWith(context.Background(), repoRoot, []Extractor{tc.ex})
			if err == nil {
				t.Fatalf("an invalid graph was returned: %+v", g)
			}
			if !errors.Is(err, ErrInvalidGraph) {
				t.Errorf("got %v, want ErrInvalidGraph", err)
			}
		})
	}
}

// §III makes an extractor failure fatal. A partial graph is worse than none:
// a consumer cannot tell a missing subtree from one that does not exist.
func TestExtractorFailureAborts(t *testing.T) {
	boom := errors.New("the tool exploded")
	g, err := BuildWith(context.Background(), repoRoot, []Extractor{
		fake{id: "TA", nodes: []graph.Node{node("package:a")}},
		fake{id: "TB", err: boom},
	})
	if err == nil {
		t.Fatalf("a failing extractor produced a graph: %+v", g)
	}
	if !errors.Is(err, ErrExtract) {
		t.Errorf("got %v, want ErrExtract", err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("the underlying cause was lost: %v", err)
	}
	if !strings.Contains(err.Error(), "TB") {
		t.Errorf("the error does not name the extractor that failed: %v", err)
	}
}

// An empty registry would emit an empty graph, indistinguishable from a
// repository that genuinely has no packages.
func TestNoExtractorIsAnError(t *testing.T) {
	if _, err := BuildWith(context.Background(), repoRoot, nil); !errors.Is(err, ErrNoExtractor) {
		t.Errorf("got %v, want ErrNoExtractor", err)
	}
	if _, err := BuildWith(context.Background(), repoRoot, []Extractor{}); !errors.Is(err, ErrNoExtractor) {
		t.Errorf("got %v, want ErrNoExtractor", err)
	}
}

// A graph that cannot say which tree produced it cannot be checked for
// staleness, so a missing commit is fatal rather than blank.
func TestCommitFailureIsFatal(t *testing.T) {
	dir := t.TempDir() // not a git repository
	g, err := BuildWith(context.Background(), dir, []Extractor{fake{id: "TA", nodes: []graph.Node{node("package:a")}}})
	if err == nil {
		t.Fatalf("a graph was built with no commit: %+v", g)
	}
	if !errors.Is(err, ErrCommit) {
		t.Errorf("got %v, want ErrCommit", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the error does not name the root it failed under: %v", err)
	}
}

// The commit is read before any extractor runs, so a repository with no HEAD
// fails fast rather than after the expensive work.
func TestCancelledContextFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Build(ctx, repoRoot); err == nil {
		t.Error("a cancelled context produced no error")
	}
}
