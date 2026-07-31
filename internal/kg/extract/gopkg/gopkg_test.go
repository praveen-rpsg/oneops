package gopkg

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rpsg/oneops/internal/kg/graph"
	"github.com/rpsg/oneops/internal/kg/model"
)

var update = flag.Bool("update", false, "regenerate the E1 golden graph")

const (
	repoRoot    = "../../../.."
	fixtureRoot = repoRoot + "/testdata/kg/fixtures/gomod"
	goldenPath  = repoRoot + "/testdata/kg/golden/gopkg_fixture.json"
)

// extractFixture runs E1 over the miniature module in testdata.
//
// The repository carries a go.work, which does not list the fixture, so the go
// tool refuses to walk it. Turning the workspace off is a property of running
// a module that is deliberately outside it, not something the extractor should
// know about, so the environment is set here rather than in production code.
func extractFixture(t *testing.T) ([]graph.Node, []graph.Edge) {
	t.Helper()
	t.Setenv("GOWORK", "off")
	nodes, edges, err := (Extractor{}).Extract(context.Background(), fixtureRoot)
	if err != nil {
		t.Fatalf("extract fixture: %v", err)
	}
	return nodes, edges
}

func asGraph(nodes []graph.Node, edges []graph.Edge) *graph.Graph {
	return &graph.Graph{SchemaVersion: graph.SchemaVersion, Nodes: nodes, Edges: edges}
}

// ---------------------------------------------------------------- positive

func TestExtractorID(t *testing.T) {
	if got := (Extractor{}).ID(); got != "E1" {
		t.Errorf("ID() = %q, want %q — §III's table names this extractor E1", got, "E1")
	}
}

// The acceptance criterion: one node per package `go list` reports.
func TestPackageCountMatchesGoList(t *testing.T) {
	// Run go list where the extractor runs it. Executed from the test's own
	// directory it would list one package, and the comparison would be against
	// the wrong number.
	cmd := exec.Command("go", "list", "./...")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	want := strings.Fields(strings.TrimSpace(string(out)))

	nodes, _, err := (Extractor{}).Extract(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(nodes) != len(want) {
		t.Fatalf("extracted %d package nodes, `go list ./...` reports %d", len(nodes), len(want))
	}
	got := make([]string, len(nodes))
	for i, n := range nodes {
		got[i] = strings.TrimPrefix(n.ID, "package:")
	}
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the extracted package set differs from go list.\ngot:  %v\nwant: %v", got, want)
	}
	t.Logf("packages extracted: %d", len(nodes))
}

// The graph E1 produces over the real repository must satisfy §II. This is the
// contract with S1.1: the validator is the authority and E1 feeds it.
func TestRepositoryExtractionIsAValidGraph(t *testing.T) {
	nodes, edges, err := (Extractor{}).Extract(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if err := asGraph(nodes, edges).Validate(); err != nil {
		t.Fatalf("E1's output does not satisfy the graph invariants: %v", err)
	}
	t.Logf("nodes: %d, edges: %d", len(nodes), len(edges))
}

// Every Evidence.Source must be in Amendment A1's canonical form. graph.Validate
// enforces this too; asserted here as well so a violation names E1 rather than
// surfacing later as a validation failure with no author.
func TestEvidenceSourcesAreRepositoryRelative(t *testing.T) {
	nodes, edges, err := (Extractor{}).Extract(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	check := func(where string, ev []graph.Evidence) {
		if len(ev) == 0 {
			t.Errorf("%s carries no evidence", where)
		}
		for _, e := range ev {
			switch {
			case e.Source == "":
				t.Errorf("%s has empty evidence source", where)
			case strings.HasPrefix(e.Source, "/"):
				t.Errorf("%s has absolute source %q (A1)", where, e.Source)
			case strings.HasPrefix(e.Source, "./"):
				t.Errorf("%s has a leading \"./\" in %q (A1)", where, e.Source)
			case strings.Contains(e.Source, `\`):
				t.Errorf("%s uses backslashes in %q (A1)", where, e.Source)
			case strings.Contains(e.Source, "Users") || strings.Contains(e.Source, "home"):
				t.Errorf("%s leaks a machine path: %q", where, e.Source)
			}
			if e.Rule == "" {
				t.Errorf("%s has evidence naming no rule", where)
			}
		}
	}
	for _, n := range nodes {
		check("node "+n.ID, n.Evidence)
	}
	for _, e := range edges {
		check("edge "+e.From+"->"+e.To, e.Evidence)
	}
}

// Import edges are reproduced exactly, checked against a package whose imports
// this story already knows: graph imports model, and nothing else in-module.
func TestImportEdgesAreReproduced(t *testing.T) {
	_, edges, err := (Extractor{}).Extract(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	const from = "package:github.com/rpsg/oneops/internal/kg/graph"
	var got []string
	for _, e := range edges {
		if e.From == from {
			got = append(got, e.To)
		}
	}
	want := []string{"package:github.com/rpsg/oneops/internal/kg/model"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("edges from %s:\ngot:  %v\nwant: %v", from, got, want)
	}
}

// Stdlib and external imports are dropped, not emitted as dangling edges.
func TestExternalImportsAreNotEdges(t *testing.T) {
	nodes, edges, err := (Extractor{}).Extract(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	present := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		present[n.ID] = true
	}
	for _, e := range edges {
		if !present[e.To] {
			t.Errorf("edge to %q, which is not a node — §II forbids a dangling endpoint", e.To)
		}
		if !strings.HasPrefix(e.To, "package:github.com/rpsg/oneops/") {
			t.Errorf("edge to %q, which is outside the module", e.To)
		}
	}
}

// A test-only package must still appear.
//
// internal/arch has no GoFiles and no Imports; a file-anchored evidence record
// would have had nothing to point at, and an extractor keyed on Imports would
// have dropped the package entirely.
func TestTestOnlyPackageIsExtracted(t *testing.T) {
	nodes, _, err := (Extractor{}).Extract(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	const want = "package:github.com/rpsg/oneops/internal/arch"
	for _, n := range nodes {
		if n.ID != want {
			continue
		}
		if n.Attrs["dir"] != "internal/arch" {
			t.Errorf("dir attr = %q, want %q", n.Attrs["dir"], "internal/arch")
		}
		if len(n.Evidence) != 1 || n.Evidence[0].Source != "internal/arch" {
			t.Errorf("evidence = %+v, want one record sourced at the directory", n.Evidence)
		}
		return
	}
	t.Errorf("%s was not extracted; a package with only test files is still a package", want)
}

// Repeated extraction over an unchanged tree must not vary — the property the
// whole regeneration model rests on.
func TestExtractionIsDeterministic(t *testing.T) {
	first, firstEdges, err := (Extractor{}).Extract(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	wantJSON, err := json.Marshal(asGraph(first, firstEdges))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for i := 0; i < 3; i++ {
		nodes, edges, err := (Extractor{}).Extract(context.Background(), repoRoot)
		if err != nil {
			t.Fatalf("extract %d: %v", i, err)
		}
		if !reflect.DeepEqual(first, nodes) || !reflect.DeepEqual(firstEdges, edges) {
			t.Fatalf("run %d produced a different graph", i)
		}
		gotJSON, err := json.Marshal(asGraph(nodes, edges))
		if err != nil {
			t.Fatalf("marshal %d: %v", i, err)
		}
		if string(gotJSON) != string(wantJSON) {
			t.Fatalf("run %d encoded to different bytes", i)
		}
	}
}

// Sorting is asserted, not assumed: go list happens to emit packages in order,
// so a missing sort would pass unnoticed until some other tool did not.
func TestOutputIsCanonicallySorted(t *testing.T) {
	nodes, edges, err := (Extractor{}).Extract(context.Background(), repoRoot)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if !slices.IsSortedFunc(nodes, func(a, b graph.Node) int { return strings.Compare(a.ID, b.ID) }) {
		t.Error("nodes are not sorted by ID")
	}
	sorted := slices.IsSortedFunc(edges, func(a, b graph.Edge) int {
		if c := strings.Compare(a.From, b.From); c != 0 {
			return c
		}
		return strings.Compare(a.To, b.To)
	})
	if !sorted {
		t.Error("edges are not sorted by From, To")
	}
}

func TestGoldenFixtureGraph(t *testing.T) {
	nodes, edges := extractFixture(t)
	g := asGraph(nodes, edges)
	if err := g.Validate(); err != nil {
		t.Fatalf("the fixture graph does not satisfy §II: %v", err)
	}

	got, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got = append(got, '\n')

	if *update {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("golden regenerated: %s", goldenPath)
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v (run with -update to create it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("E1's output drifted from the golden graph.\n got: %s\nwant: %s", got, want)
	}
}

// The fixture's shape is asserted independently of the golden bytes, so a
// mistakenly regenerated golden cannot quietly redefine what E1 should produce.
func TestFixtureShape(t *testing.T) {
	nodes, edges := extractFixture(t)

	wantNodes := []string{
		"package:example.test/fixture/alpha",
		"package:example.test/fixture/beta",
		"package:example.test/fixture/gamma",
		"package:example.test/fixture/testonly",
	}
	got := make([]string, len(nodes))
	for i, n := range nodes {
		got[i] = n.ID
	}
	if !reflect.DeepEqual(got, wantNodes) {
		t.Errorf("nodes:\ngot:  %v\nwant: %v", got, wantNodes)
	}

	// alpha->beta, gamma->alpha, gamma->beta. fmt and strings are dropped;
	// testonly has no non-test imports at all.
	wantEdges := [][2]string{
		{"package:example.test/fixture/alpha", "package:example.test/fixture/beta"},
		{"package:example.test/fixture/gamma", "package:example.test/fixture/alpha"},
		{"package:example.test/fixture/gamma", "package:example.test/fixture/beta"},
	}
	if len(edges) != len(wantEdges) {
		t.Fatalf("got %d edges, want %d: %+v", len(edges), len(wantEdges), edges)
	}
	for i, w := range wantEdges {
		if edges[i].From != w[0] || edges[i].To != w[1] {
			t.Errorf("edge %d = %s->%s, want %s->%s", i, edges[i].From, edges[i].To, w[0], w[1])
		}
		if edges[i].Kind != "imports" {
			t.Errorf("edge %d kind = %q, want %q", i, edges[i].Kind, "imports")
		}
	}
}

func TestProvenanceIsDerivedAndCertain(t *testing.T) {
	nodes, edges := extractFixture(t)
	for _, n := range nodes {
		if n.Origin != model.OriginDerived || n.Confidence != model.ConfidenceCertain {
			t.Errorf("node %s: origin=%q confidence=%q, want derived/certain — go list is an "+
				"executable artifact", n.ID, n.Origin, n.Confidence)
		}
	}
	for _, e := range edges {
		if e.Origin != model.OriginDerived || e.Confidence != model.ConfidenceCertain {
			t.Errorf("edge %s->%s: origin=%q confidence=%q, want derived/certain",
				e.From, e.To, e.Origin, e.Confidence)
		}
	}
}

// §III budgets E1 at under two seconds.
func TestExtractionIsWithinBudget(t *testing.T) {
	if _, _, err := (Extractor{}).Extract(context.Background(), repoRoot); err != nil {
		t.Fatalf("warm: %v", err)
	}
	start := time.Now()
	if _, _, err := (Extractor{}).Extract(context.Background(), repoRoot); err != nil {
		t.Fatalf("extract: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("extraction took %v, over §III's 2s budget", elapsed)
	} else {
		t.Logf("extraction took %v", elapsed)
	}
}

// ---------------------------------------------------------------- negative

// §III makes a non-zero `go list` fatal: a partial list would produce a graph
// missing whole subtrees, indistinguishable from a small module.
func TestGoListFailureIsFatal(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte("this is not go"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Setenv("GOWORK", "off")
	nodes, edges, err := (Extractor{}).Extract(context.Background(), dir)
	if err == nil {
		t.Fatalf("a directory that is not a module extracted %d nodes, %d edges", len(nodes), len(edges))
	}
	if !errors.Is(err, ErrGoList) {
		t.Errorf("got %v, want ErrGoList", err)
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the error does not name the directory it failed in: %v", err)
	}
}

func TestCancelledContextFails(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := (Extractor{}).Extract(ctx, repoRoot); err == nil {
		t.Error("extraction with a cancelled context returned no error")
	}
}

func TestBuildRejects(t *testing.T) {
	root := "/repo"
	cases := []struct {
		name  string
		input string
		want  error
	}{
		{"malformed JSON", `{"ImportPath": "a/b", `, ErrMalformedOutput},
		{"not JSON", `go: cannot find module`, ErrMalformedOutput},
		{"truncated stream", `{"ImportPath":"a/b","Dir":"/repo/b"}{"ImportPath":"a/c"`, ErrMalformedOutput},
		{"wrong type for Imports", `{"ImportPath":"a/b","Dir":"/repo/b","Imports":"x"}`, ErrMalformedOutput},
		{"package without ImportPath", `{"Dir":"/repo/b"}`, ErrMalformedOutput},
		{"package without Dir", `{"ImportPath":"a/b"}`, ErrMalformedOutput},
		{"duplicate package", `{"ImportPath":"a/b","Dir":"/repo/b"}{"ImportPath":"a/b","Dir":"/repo/b"}`, ErrMalformedOutput},
		{"directory outside the root", `{"ImportPath":"a/b","Dir":"/elsewhere/b"}`, ErrPathOutsideRoot},
		{"directory escaping via ..", `{"ImportPath":"a/b","Dir":"/repo/../other/b"}`, ErrPathOutsideRoot},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nodes, edges, err := build(root, strings.NewReader(tc.input))
			if err == nil {
				t.Fatalf("accepted %q: %d nodes, %d edges", tc.input, len(nodes), len(edges))
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("got %v, want an error matching %v", err, tc.want)
			}
		})
	}
}

// An empty stream is a module with no packages, not a fault. `go list` exits
// zero for it, so E1 must not invent a failure the tool did not report.
func TestEmptyOutputYieldsAnEmptyGraph(t *testing.T) {
	nodes, edges, err := build("/repo", strings.NewReader(""))
	if err != nil {
		t.Fatalf("an empty package list is not an error: %v", err)
	}
	if len(nodes) != 0 || len(edges) != 0 {
		t.Errorf("got %d nodes and %d edges from empty output", len(nodes), len(edges))
	}
	if err := asGraph(nodes, edges).Validate(); err != nil {
		t.Errorf("an empty graph must still validate: %v", err)
	}
}

// Paths are normalised even when go list reports them with separators or
// prefixes A1 forbids.
func TestPathNormalisation(t *testing.T) {
	nodes, _, err := build("/repo", strings.NewReader(
		`{"ImportPath":"a/b","Dir":"/repo/internal/b","Name":"b"}`+
			`{"ImportPath":"a/c","Dir":"/repo/./internal/c","Name":"c"}`))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := []string{"internal/b", "internal/c"}
	for i, n := range nodes {
		if n.Attrs["dir"] != want[i] {
			t.Errorf("node %s dir = %q, want %q", n.ID, n.Attrs["dir"], want[i])
		}
		if n.Evidence[0].Source != want[i] {
			t.Errorf("node %s evidence source = %q, want %q", n.ID, n.Evidence[0].Source, want[i])
		}
	}
}

// The golden comparison must be able to fail.
func TestGoldenMismatchIsDetected(t *testing.T) {
	nodes, edges := extractFixture(t)
	g := asGraph(nodes, edges)
	g.Nodes[0].Attrs["dir"] = "somewhere/else"

	got, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if string(append(got, '\n')) == string(want) {
		t.Error("a changed dir produced the golden bytes; the comparison is blind")
	}
}
