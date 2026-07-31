package storage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rpsg/oneops/internal/kg/graph"
	"github.com/rpsg/oneops/internal/kg/model"
)

const goldenPath = "../../../testdata/kg/golden/graph_minimal.json"

// sample is a graph exercising every field the model has, including the two
// that are omitted when empty (a Node's Attrs, an Evidence's Line) and the one
// exemption from the evidence invariant (origin=declared).
func sample() *graph.Graph {
	return &graph.Graph{
		SchemaVersion: graph.SchemaVersion,
		Commit:        "89abcdef0123456789abcdef0123456789abcdef",
		Nodes: []graph.Node{
			{
				ID:         "guard:TestAuditAppend_TakesTheChainHeadLock",
				Kind:       "guard",
				Attrs:      map[string]string{"zeta": "last", "alpha": "first", "mu": "middle"},
				Evidence:   []graph.Evidence{{Source: "internal/arch/scope_completeness_test.go", Line: 399, Rule: "E5.guard"}},
				Origin:     model.OriginDerived,
				Confidence: model.ConfidenceCertain,
			},
			{
				ID:         "route:GET /healthz",
				Kind:       "route",
				Evidence:   []graph.Evidence{{Source: "internal/httpapi/server.go", Rule: "E2.route"}},
				Origin:     model.OriginDerived,
				Confidence: model.ConfidenceCertain,
			},
			{
				ID:         "team:platform",
				Kind:       "team",
				Origin:     model.OriginDeclared,
				Confidence: model.ConfidenceHigh,
			},
		},
		Edges: []graph.Edge{
			{
				From: "guard:TestAuditAppend_TakesTheChainHeadLock", To: "route:GET /healthz", Kind: "guards",
				Evidence:   []graph.Evidence{{Source: "internal/arch/scope_completeness_test.go", Line: 406, Rule: "E5.subject"}},
				Origin:     model.OriginDerived,
				Confidence: model.ConfidenceCertain,
			},
		},
	}
}

func init() {
	if err := sample().Validate(); err != nil {
		panic("the storage test fixture is not a valid graph: " + err.Error())
	}
}

// ---------------------------------------------------------------- positive

// The golden fixture accepted at S1.1 must be exactly what this package emits.
//
// Decoding then re-encoding it proves both directions against the same bytes
// without restating the fixture here: if Encode's format drifted, or Decode
// dropped a field, the bytes would differ.
func TestGoldenFixtureRoundTripsByteIdentically(t *testing.T) {
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	g, err := Decode(want)
	if err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	got, err := Encode(g)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("storage does not reproduce the accepted golden fixture.\n got: %s\nwant: %s", got, want)
	}
}

// A graph that came out of storage must still satisfy §II.
func TestGoldenFixtureIsAValidGraph(t *testing.T) {
	g, err := ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := g.Validate(); err != nil {
		t.Errorf("the golden fixture no longer validates after loading: %v", err)
	}
}

func TestFileRoundTripPreservesEveryField(t *testing.T) {
	path := filepath.Join(t.TempDir(), Filename)
	in := sample()
	if err := WriteFile(path, in); err != nil {
		t.Fatalf("write: %v", err)
	}
	out, err := ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("the graph did not survive the file.\n in: %+v\nout: %+v", in, out)
	}
}

// write -> read -> write must produce the same file, byte for byte.
func TestWriteReadWriteIsByteIdentical(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.json")
	second := filepath.Join(dir, "second.json")

	if err := WriteFile(first, sample()); err != nil {
		t.Fatalf("write first: %v", err)
	}
	g, err := ReadFile(first)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := WriteFile(second, g); err != nil {
		t.Fatalf("write second: %v", err)
	}

	a, _ := os.ReadFile(first)
	b, _ := os.ReadFile(second)
	if !bytes.Equal(a, b) {
		t.Errorf("write->read->write is not byte-stable.\nfirst:  %s\nsecond: %s", a, b)
	}
}

// Repeated encodings of one graph must not vary.
//
// Attrs is a map and Go randomises map iteration deliberately. encoding/json
// sorts object keys, so ordering cannot leak into the file — asserted over many
// iterations rather than assumed, because a single run would pass by luck.
func TestEncodeIsDeterministic(t *testing.T) {
	first, err := Encode(sample())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	for i := 0; i < 64; i++ {
		again, err := Encode(sample())
		if err != nil {
			t.Fatalf("encode %d: %v", i, err)
		}
		if !bytes.Equal(first, again) {
			t.Fatalf("encoding varies between runs at iteration %d:\n a: %s\n b: %s", i, first, again)
		}
	}
}

// The map keys the specification calls "sorted" are a Node's Attrs, which is
// the only structure in the model whose order is not fixed by declaration.
func TestAttrKeysAreSorted(t *testing.T) {
	out, err := Encode(sample())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	alpha := bytes.Index(out, []byte(`"alpha"`))
	mu := bytes.Index(out, []byte(`"mu"`))
	zeta := bytes.Index(out, []byte(`"zeta"`))
	if alpha < 0 || mu < 0 || zeta < 0 {
		t.Fatalf("attrs missing from output: %s", out)
	}
	if alpha >= mu || mu >= zeta {
		t.Errorf("attr keys are not sorted (alpha=%d mu=%d zeta=%d): %s", alpha, mu, zeta, out)
	}
}

func TestEncodeFormat(t *testing.T) {
	out, err := Encode(sample())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !bytes.HasSuffix(out, []byte("\n")) {
		t.Error("output has no trailing newline (§VII)")
	}
	if bytes.HasSuffix(out, []byte("\n\n")) {
		t.Error("output has more than one trailing newline")
	}
	if !bytes.Contains(out, []byte("\n  \"schema_version\"")) {
		t.Errorf("output is not indented with two spaces: %s", out)
	}
	if bytes.Contains(out, []byte("\t")) {
		t.Error("output contains a tab; §VII requires two-space indentation")
	}
}

// ---------------------------------------------------------------- negative

func TestDecodeRejects(t *testing.T) {
	cases := []struct {
		name string
		data string
		want error // nil means "any error"
	}{
		{"malformed JSON", `{"schema_version": 1, "nodes": [`, nil},
		{"not JSON at all", `this is not json`, nil},
		{"empty file", ``, nil},
		{"truncated mid-object", `{"schema_version":1,"commit":"abc","nodes":[{"id":"a","kin`, nil},
		{"schema version newer than supported", `{"schema_version":2,"commit":"","nodes":[],"edges":[]}`, ErrUnsupportedSchemaVersion},
		{"schema version far in the future", `{"schema_version":99,"commit":"","nodes":[],"edges":[]}`, ErrUnsupportedSchemaVersion},
		{"schema version zero", `{"schema_version":0,"commit":"","nodes":[],"edges":[]}`, ErrUnsupportedSchemaVersion},
		{"schema version absent", `{"commit":"abc","nodes":[],"edges":[]}`, ErrUnsupportedSchemaVersion},
		{"schema version negative", `{"schema_version":-1,"nodes":[],"edges":[]}`, ErrUnsupportedSchemaVersion},
		{"unknown top-level field", `{"schema_version":1,"commit":"a","nodes":[],"edges":[],"extra":1}`, nil},
		{"unknown node field", `{"schema_version":1,"commit":"a","edges":[],"nodes":[{"id":"a","kind":"b","weight":3}]}`, nil},
		{"trailing content", `{"schema_version":1,"commit":"a","nodes":[],"edges":[]}{"schema_version":1}`, nil},
		{"wrong type for nodes", `{"schema_version":1,"commit":"a","nodes":"lots","edges":[]}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, err := Decode([]byte(tc.data))
			if err == nil {
				t.Fatalf("accepted %q and returned %+v", tc.data, g)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("got %v, want an error matching %v", err, tc.want)
			}
		})
	}
}

// A version this build cannot read is a version it must not write either:
// emitting one would produce a file the same binary refuses to load back.
func TestEncodeRejectsUnsupportedVersion(t *testing.T) {
	for _, v := range []int{0, -1, graph.SchemaVersion + 1, 99} {
		g := sample()
		g.SchemaVersion = v
		if _, err := Encode(g); !errors.Is(err, ErrUnsupportedSchemaVersion) {
			t.Errorf("Encode accepted schema version %d: %v", v, err)
		}
	}
}

// The boundary between storage and graph, asserted rather than assumed.
//
// A file can be flawless storage and a broken graph. Storage loads it — that is
// how a caller sees what is wrong — and graph.Validate is what reports it.
// Duplicating the invariants here would give two answers to one question.
func TestStorageLoadsAGraphThatFailsItsInvariants(t *testing.T) {
	// Well-formed JSON, supported version, but the node has no evidence, the
	// edge points nowhere, and the nodes are out of order.
	data := `{
  "schema_version": 1,
  "commit": "abc",
  "nodes": [
    {"id": "z:second", "kind": "k", "evidence": [], "origin": "derived", "confidence": "certain"},
    {"id": "a:first", "kind": "k", "evidence": [], "origin": "derived", "confidence": "certain"}
  ],
  "edges": [
    {"from": "a:first", "to": "missing:node", "kind": "cites", "evidence": [], "origin": "derived", "confidence": "certain"}
  ]
}`
	g, err := Decode([]byte(data))
	if err != nil {
		t.Fatalf("storage refused a well-formed file: %v", err)
	}
	err = g.Validate()
	for _, want := range []error{graph.ErrMissingEvidence, graph.ErrDanglingEdge, graph.ErrUnsorted} {
		if !errors.Is(err, want) {
			t.Errorf("graph.Validate did not report %v: %v", want, err)
		}
	}
}

// Missing required fields survive the load as zero values and are caught by
// graph.Validate, not by storage.
func TestMissingRequiredFieldsAreCaughtByTheGraph(t *testing.T) {
	data := `{"schema_version":1,"commit":"abc","nodes":[{"id":"","kind":""}],"edges":[]}`
	g, err := Decode([]byte(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	err = g.Validate()
	for _, want := range []error{graph.ErrMissingID, graph.ErrMissingKind, graph.ErrUnknownOrigin} {
		if !errors.Is(err, want) {
			t.Errorf("graph.Validate did not report %v: %v", want, err)
		}
	}
}

// The golden comparison must actually detect a difference.
//
// Without this the golden test above would pass just as happily if Encode
// returned the file it was comparing against.
func TestGoldenMismatchIsDetected(t *testing.T) {
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	g, err := Decode(want)
	if err != nil {
		t.Fatalf("decode golden: %v", err)
	}
	g.Nodes[0].Confidence = model.ConfidenceMedium

	got, err := Encode(g)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if bytes.Equal(got, want) {
		t.Error("a changed confidence produced identical bytes; the golden comparison is blind")
	}
}

func TestReadFileErrors(t *testing.T) {
	if _, err := ReadFile(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Error("reading a file that does not exist returned no error")
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, Filename)
	if err := os.WriteFile(bad, []byte(`{"schema_version":7}`), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := ReadFile(bad); !errors.Is(err, ErrUnsupportedSchemaVersion) {
		t.Errorf("ReadFile on a future-version file: got %v, want ErrUnsupportedSchemaVersion", err)
	}
}

// The version error must say what was found and what is carried; a bare
// "unsupported" leaves an operator guessing which side is stale.
func TestVersionErrorNamesBothVersions(t *testing.T) {
	_, err := Decode([]byte(`{"schema_version":42,"nodes":[],"edges":[]}`))
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "42") || !strings.Contains(err.Error(), "1") {
		t.Errorf("error does not name both versions: %v", err)
	}
}
