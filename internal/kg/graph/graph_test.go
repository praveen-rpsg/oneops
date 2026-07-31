package graph

import (
	"encoding/json"
	"errors"
	"flag"
	"os"
	"testing"

	"github.com/rpsg/oneops/internal/kg/model"
)

var update = flag.Bool("update", false, "regenerate the golden graph fixture")

const goldenPath = "../../../testdata/kg/golden/graph_minimal.json"

// valid returns a graph that satisfies every §II invariant. Each negative test
// breaks exactly one thing about it, so a failure names the invariant it broke
// rather than whatever else happened to be wrong.
func valid() *Graph {
	return &Graph{
		SchemaVersion: SchemaVersion,
		Commit:        "0123456789abcdef0123456789abcdef01234567",
		Nodes: []Node{
			{
				ID:   "package:internal/kg/graph",
				Kind: "package",
				Attrs: map[string]string{
					"name":     "graph",
					"dir":      "internal/kg/graph",
					"stdlib":   "false",
					"exported": "true",
				},
				Evidence:   []Evidence{{Source: "internal/kg/graph/graph.go", Line: 1, Rule: "E1.package"}},
				Origin:     model.OriginDerived,
				Confidence: model.ConfidenceCertain,
			},
			{
				ID:         "package:internal/kg/model",
				Kind:       "package",
				Evidence:   []Evidence{{Source: "internal/kg/model/model.go", Rule: "E1.package"}},
				Origin:     model.OriginDerived,
				Confidence: model.ConfidenceCertain,
			},
			{
				// A declaration carries no evidence: the .pkg/ file is the
				// evidence, which is why §II exempts origin=declared. Its ID
				// sorts last, so the derived nodes keep indices 0 and 1.
				ID:         "team:platform",
				Kind:       "team",
				Origin:     model.OriginDeclared,
				Confidence: model.ConfidenceHigh,
			},
		},
		Edges: []Edge{
			{
				From: "package:internal/kg/graph", To: "package:internal/kg/model", Kind: "imports",
				Evidence:   []Evidence{{Source: "internal/kg/graph/graph.go", Line: 20, Rule: "E1.import"}},
				Origin:     model.OriginDerived,
				Confidence: model.ConfidenceCertain,
			},
		},
	}
}

func init() {
	// The fixture is sorted by construction. If an edit breaks that, every
	// negative test below would fail for the wrong reason.
	if err := valid().Validate(); err != nil {
		panic("the test fixture is not a valid graph: " + err.Error())
	}
}

// ---------------------------------------------------------------- positive

func TestValidGraphPasses(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("a graph satisfying every §II invariant was rejected: %v", err)
	}
}

// A declared node needs no evidence; a derived one does. Asserted together so a
// change that made evidence unconditional would fail here rather than silently
// making declarations unexpressible.
func TestDeclaredNodeNeedsNoEvidence(t *testing.T) {
	g := valid()
	g.Nodes = g.Nodes[2:] // the declared team, alone
	g.Edges = nil
	if err := g.Validate(); err != nil {
		t.Fatalf("a declared node with no evidence was rejected: %v", err)
	}
	g.Nodes[0].Origin = model.OriginDerived
	if err := g.Validate(); !errors.Is(err, ErrMissingEvidence) {
		t.Errorf("the same node as derived: got %v, want ErrMissingEvidence", err)
	}
}

// The regression test for Amendment A3 §C2.
//
// The superseded declaration wrote both endpoints as one field group with a
// single tag; encoding/json resolved both to the name "from", treated that as a
// conflict, and dropped both. A fully populated edge marshalled to
// {"kind":"imports"}. Every in-memory test still passed, and the loss would
// first have surfaced as missing edges long after S1.1.
func TestEdgeEndpointsSurviveSerialisation(t *testing.T) {
	in := valid().Edges[0]
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keyed); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	for _, key := range []string{"from", "to", "kind"} {
		if _, ok := keyed[key]; !ok {
			t.Errorf("marshalled edge has no %q key: %s — this is the A3 §C2 defect", key, raw)
		}
	}

	var back Edge
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.From != in.From || back.To != in.To {
		t.Errorf("endpoints did not round-trip: got From=%q To=%q, want From=%q To=%q",
			back.From, back.To, in.From, in.To)
	}
}

func TestGraphRoundTrips(t *testing.T) {
	in := valid()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Graph
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := back.Validate(); err != nil {
		t.Errorf("a graph that round-tripped no longer validates: %v", err)
	}
	again, err := json.Marshal(&back)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(raw) != string(again) {
		t.Errorf("round-trip is not byte-stable:\n first: %s\nsecond: %s", raw, again)
	}
}

// Determinism: two independently constructed graphs over the same facts must
// serialise to identical bytes.
//
// Attrs is a map, and Go randomises map iteration on purpose. encoding/json
// sorts object keys, so the map cannot leak ordering into the output — this
// asserts that rather than assuming it, because the whole regeneration model
// rests on two runs producing the same bytes (§VII).
func TestTwoBuildsAreByteIdentical(t *testing.T) {
	for i := 0; i < 64; i++ {
		a, err := json.Marshal(valid())
		if err != nil {
			t.Fatalf("marshal a: %v", err)
		}
		b, err := json.Marshal(valid())
		if err != nil {
			t.Fatalf("marshal b: %v", err)
		}
		if string(a) != string(b) {
			t.Fatalf("independent builds differ on iteration %d:\n a: %s\n b: %s", i, a, b)
		}
	}
}

func TestGoldenGraph(t *testing.T) {
	got, err := json.MarshalIndent(valid(), "", "  ")
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
		t.Errorf("graph serialisation drifted from the golden fixture.\n got: %s\nwant: %s", got, want)
	}
}

// ---------------------------------------------------------------- negative

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Graph)
		want   error
	}{
		{"duplicate node ID", func(g *Graph) {
			g.Nodes[1].ID = g.Nodes[0].ID
		}, ErrDuplicateNodeID},

		{"dangling edge From", func(g *Graph) {
			g.Edges[0].From = "package:does/not/exist"
		}, ErrDanglingEdge},

		{"dangling edge To", func(g *Graph) {
			g.Edges[0].To = "package:does/not/exist"
		}, ErrDanglingEdge},

		{"invalid node origin", func(g *Graph) {
			g.Nodes[0].Origin = "guessed"
		}, ErrUnknownOrigin},

		{"empty node origin", func(g *Graph) {
			g.Nodes[0].Origin = ""
		}, ErrUnknownOrigin},

		{"invalid edge origin", func(g *Graph) {
			g.Edges[0].Origin = "assumed"
		}, ErrUnknownOrigin},

		{"invalid node confidence", func(g *Graph) {
			g.Nodes[0].Confidence = "quite sure"
		}, ErrUnknownConfidence},

		{"empty edge confidence", func(g *Graph) {
			g.Edges[0].Confidence = ""
		}, ErrUnknownConfidence},

		{"derived node without evidence", func(g *Graph) {
			g.Nodes[0].Evidence = nil
		}, ErrMissingEvidence},

		{"evidence without source", func(g *Graph) {
			g.Nodes[0].Evidence[0].Source = ""
		}, ErrMalformedEvidence},

		{"evidence without rule", func(g *Graph) {
			g.Nodes[0].Evidence[0].Rule = ""
		}, ErrMalformedEvidence},

		{"evidence with absolute source (A1)", func(g *Graph) {
			g.Nodes[0].Evidence[0].Source = "/Users/someone/oneops/internal/kg/graph/graph.go"
		}, ErrMalformedEvidence},

		{"evidence with leading ./ (A1)", func(g *Graph) {
			g.Nodes[0].Evidence[0].Source = "./internal/kg/graph/graph.go"
		}, ErrMalformedEvidence},

		{"evidence with backslashes (A1)", func(g *Graph) {
			g.Nodes[0].Evidence[0].Source = `internal\kg\graph\graph.go`
		}, ErrMalformedEvidence},

		{"evidence with negative line", func(g *Graph) {
			g.Nodes[0].Evidence[0].Line = -1
		}, ErrMalformedEvidence},

		{"edge evidence malformed", func(g *Graph) {
			g.Edges[0].Evidence[0].Rule = ""
		}, ErrMalformedEvidence},

		{"node without ID", func(g *Graph) {
			g.Nodes[0].ID = ""
		}, ErrMissingID},

		{"node without kind", func(g *Graph) {
			g.Nodes[0].Kind = ""
		}, ErrMissingKind},

		{"edge without kind", func(g *Graph) {
			g.Edges[0].Kind = ""
		}, ErrMissingKind},

		{"unsorted nodes", func(g *Graph) {
			g.Nodes[0], g.Nodes[1] = g.Nodes[1], g.Nodes[0]
		}, ErrUnsorted},

		{"schema version zero", func(g *Graph) {
			g.SchemaVersion = 0
		}, ErrSchemaVersion},

		{"schema version negative", func(g *Graph) {
			g.SchemaVersion = -1
		}, ErrSchemaVersion},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := valid()
			tc.mutate(g)
			err := g.Validate()
			if err == nil {
				t.Fatalf("%s was accepted; want %v", tc.name, tc.want)
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("got %v, want an error matching %v", err, tc.want)
			}
		})
	}
}

// Unsorted edges need three of them to detect, so it is its own case.
func TestUnsortedEdgesAreRejected(t *testing.T) {
	g := valid()
	g.Edges = []Edge{
		{From: "package:internal/kg/model", To: "package:internal/kg/graph", Kind: "imports",
			Evidence: []Evidence{{Source: "internal/kg/model/model.go", Rule: "E1.import"}},
			Origin:   model.OriginDerived, Confidence: model.ConfidenceCertain},
		{From: "package:internal/kg/graph", To: "package:internal/kg/model", Kind: "imports",
			Evidence: []Evidence{{Source: "internal/kg/graph/graph.go", Rule: "E1.import"}},
			Origin:   model.OriginDerived, Confidence: model.ConfidenceCertain},
	}
	if err := g.Validate(); !errors.Is(err, ErrUnsorted) {
		t.Errorf("edges out of From order were accepted: %v", err)
	}
}

// Validate reports every violation, not the first. A caller repairing a
// generated graph needs the whole list, and an extractor that breaks one
// invariant usually breaks it repeatedly.
func TestValidateReportsAllViolations(t *testing.T) {
	g := valid()
	g.SchemaVersion = 0
	g.Nodes[1].ID = g.Nodes[0].ID
	g.Nodes[0].Origin = "guessed"
	g.Edges[0].To = "package:does/not/exist"

	err := g.Validate()
	for _, want := range []error{ErrSchemaVersion, ErrDuplicateNodeID, ErrUnknownOrigin, ErrDanglingEdge} {
		if !errors.Is(err, want) {
			t.Errorf("four invariants were broken but %v is not reported: %v", want, err)
		}
	}
}

// An empty graph is structurally valid: nothing violates an invariant. This is
// asserted so that a future change cannot quietly turn Validate into a
// non-emptiness check, which is a Part VI concern and not this package's.
func TestEmptyGraphIsStructurallyValid(t *testing.T) {
	g := &Graph{SchemaVersion: SchemaVersion}
	if err := g.Validate(); err != nil {
		t.Errorf("an empty graph is not an invariant violation, but was rejected: %v", err)
	}
}
