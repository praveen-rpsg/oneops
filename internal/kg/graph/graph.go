// Package graph holds the knowledge graph's topology and its invariants.
//
// Pure data. Nothing here reads a file, a database, the network, the clock or
// the environment; the types are what extractors produce and what storage
// writes. Per Amendment A3 the package imports the standard library and
// internal/kg/model, and nothing else — model is beneath it in the DAG and
// never depends on it, so a cycle between the two is unrepresentable.
//
// Freshness is Graph.Commit. There is no Freshness type: under ADR-PKG-001 the
// graph is regenerated wholesale and never stored, so no node can carry a
// freshness that differs from its graph's (Amendment A3 §C3).
package graph

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/rpsg/oneops/internal/kg/model"
)

// SchemaVersion is the current graph schema. It bumps on any breaking field
// change; storage refuses to load a graph declaring a newer one (§II).
const SchemaVersion = 1

// Evidence links a fact to the repository text that proves it (§II).
type Evidence struct {
	Source string `json:"source"` // repo-relative path
	Line   int    `json:"line,omitempty"`
	Rule   string `json:"rule"` // extractor id, e.g. "E3.table"
}

// Node is one entity in the graph (§II).
type Node struct {
	ID         string            `json:"id"`   // "<kind>:<identity>", stable
	Kind       string            `json:"kind"` // package|route|table|guard|adr|...
	Attrs      map[string]string `json:"attrs,omitempty"`
	Evidence   []Evidence        `json:"evidence"`
	Origin     model.Origin      `json:"origin"`
	Confidence model.Confidence  `json:"confidence"`
}

// Edge is one typed relationship between two nodes (§II, as ratified by
// Amendment A3 §C2 — From and To carry distinct JSON names).
type Edge struct {
	From       string           `json:"from"`
	To         string           `json:"to"`
	Kind       string           `json:"kind"` // imports|serves|guards|governs|owns|cites|...
	Evidence   []Evidence       `json:"evidence"`
	Origin     model.Origin     `json:"origin"`
	Confidence model.Confidence `json:"confidence"`
}

// Graph is the whole derived knowledge graph (§II).
type Graph struct {
	SchemaVersion int    `json:"schema_version"`
	Commit        string `json:"commit"` // git rev-parse HEAD
	Nodes         []Node `json:"nodes"`  // sorted by ID
	Edges         []Edge `json:"edges"`  // sorted by From,To,Kind
}

// The violations Validate reports. Each is a sentinel so a caller can match a
// specific failure with errors.Is rather than by reading a message.
var (
	// ErrSchemaVersion is a graph declaring a version below 1.
	ErrSchemaVersion = errors.New("graph: schema version must be at least 1")
	// ErrMissingID is a node with no identity.
	ErrMissingID = errors.New("graph: node has no id")
	// ErrMissingKind is a node or edge with no kind.
	ErrMissingKind = errors.New("graph: kind is empty")
	// ErrDuplicateNodeID is two nodes claiming one identity.
	ErrDuplicateNodeID = errors.New("graph: duplicate node id")
	// ErrMissingEvidence is a derived node with nothing proving it.
	ErrMissingEvidence = errors.New("graph: node has no evidence and is not declared")
	// ErrMalformedEvidence is an evidence record that cannot be resolved.
	ErrMalformedEvidence = errors.New("graph: malformed evidence")
	// ErrDanglingEdge is an edge whose endpoint is not a node in this graph.
	ErrDanglingEdge = errors.New("graph: edge endpoint does not exist")
	// ErrUnknownOrigin is an origin outside the declared vocabulary.
	ErrUnknownOrigin = errors.New("graph: origin is not a declared value")
	// ErrUnknownConfidence is a confidence outside the declared vocabulary.
	ErrUnknownConfidence = errors.New("graph: confidence is not a declared value")
	// ErrUnsorted is a collection that is not in its canonical order.
	ErrUnsorted = errors.New("graph: collection is not sorted")
)

// Validate enforces every invariant §II places on a graph.
//
// It reports all violations rather than the first: a caller fixing a generated
// graph wants the whole list, and an extractor that breaks one invariant
// usually breaks it many times over. The result is an errors.Join, so
// errors.Is matches any sentinel present.
//
// What it deliberately does not check: whether an evidence Source names a file
// that exists, whether an inferred node has been scored certain, whether the
// commit is current. Those are Part VI validators and belong to the validate
// package — a graph that fails them is well-formed but wrong, which is a
// finding, not a structural defect.
func (g *Graph) Validate() error {
	var problems []error

	if g.SchemaVersion < 1 {
		problems = append(problems, fmt.Errorf("%w: have %d", ErrSchemaVersion, g.SchemaVersion))
	}

	ids := make(map[string]bool, len(g.Nodes))
	for i, n := range g.Nodes {
		switch {
		case n.ID == "":
			problems = append(problems, fmt.Errorf("%w: nodes[%d]", ErrMissingID, i))
		case ids[n.ID]:
			problems = append(problems, fmt.Errorf("%w: %q", ErrDuplicateNodeID, n.ID))
		default:
			ids[n.ID] = true
		}
		if n.Kind == "" {
			problems = append(problems, fmt.Errorf("%w: node %q", ErrMissingKind, n.ID))
		}
		// Evidence is what separates a derived fact from an assertion. A
		// declaration is exempt because a human wrote it: the .pkg/ file is
		// itself the evidence, and demanding more would make ownership
		// unexpressible.
		if len(n.Evidence) == 0 && n.Origin != model.OriginDeclared {
			problems = append(problems, fmt.Errorf("%w: node %q has origin %q",
				ErrMissingEvidence, n.ID, n.Origin))
		}
		problems = append(problems, evidenceProblems(n.Evidence, "node "+n.ID)...)
		problems = append(problems, provenanceProblems(n.Origin, n.Confidence, "node "+n.ID)...)
	}

	for i, e := range g.Edges {
		where := fmt.Sprintf("edge %q->%q (%s)", e.From, e.To, e.Kind)
		if e.Kind == "" {
			problems = append(problems, fmt.Errorf("%w: edges[%d]", ErrMissingKind, i))
		}
		// An edge to a node that is not here is a claim about something the
		// graph does not contain, which no consumer can resolve.
		if !ids[e.From] {
			problems = append(problems, fmt.Errorf("%w: %s has no node %q", ErrDanglingEdge, where, e.From))
		}
		if !ids[e.To] {
			problems = append(problems, fmt.Errorf("%w: %s has no node %q", ErrDanglingEdge, where, e.To))
		}
		problems = append(problems, evidenceProblems(e.Evidence, where)...)
		problems = append(problems, provenanceProblems(e.Origin, e.Confidence, where)...)
	}

	// Canonical order is what makes two runs over one tree produce identical
	// bytes. Sorting is the producer's job — §IV normalises before emit — so
	// this reports the violation rather than repairing it.
	if !slices.IsSortedFunc(g.Nodes, compareNodes) {
		problems = append(problems, fmt.Errorf("%w: nodes must ascend by ID", ErrUnsorted))
	}
	if !slices.IsSortedFunc(g.Edges, compareEdges) {
		problems = append(problems, fmt.Errorf("%w: edges must ascend by From, To, Kind", ErrUnsorted))
	}

	return errors.Join(problems...)
}

// compareNodes orders nodes by ID.
func compareNodes(a, b Node) int { return strings.Compare(a.ID, b.ID) }

// compareEdges orders edges by From, then To, then Kind.
func compareEdges(a, b Edge) int {
	if c := strings.Compare(a.From, b.From); c != 0 {
		return c
	}
	if c := strings.Compare(a.To, b.To); c != 0 {
		return c
	}
	return strings.Compare(a.Kind, b.Kind)
}

// evidenceProblems checks each record can locate the text it cites.
//
// The path rule is Amendment A1's: a Source that varies with the machine or the
// checkout location makes the graph machine-specific, which is the failure A1
// exists to prevent. Canonical form is forward-slash separated, relative to the
// repository root, with no leading "./".
func evidenceProblems(ev []Evidence, where string) []error {
	var problems []error
	for i, e := range ev {
		at := fmt.Sprintf("%s evidence[%d]", where, i)
		switch {
		case e.Source == "":
			problems = append(problems, fmt.Errorf("%w: %s has no source", ErrMalformedEvidence, at))
		case strings.HasPrefix(e.Source, "/"):
			problems = append(problems, fmt.Errorf("%w: %s source %q is absolute; A1 requires a "+
				"repository-relative path", ErrMalformedEvidence, at, e.Source))
		case strings.HasPrefix(e.Source, "./"):
			problems = append(problems, fmt.Errorf("%w: %s source %q has a leading \"./\"; A1's "+
				"canonical form has none", ErrMalformedEvidence, at, e.Source))
		case strings.Contains(e.Source, `\`):
			problems = append(problems, fmt.Errorf("%w: %s source %q is not forward-slash "+
				"separated (A1)", ErrMalformedEvidence, at, e.Source))
		}
		if e.Rule == "" {
			problems = append(problems, fmt.Errorf("%w: %s names no rule, so nothing says which "+
				"extractor is answerable for it", ErrMalformedEvidence, at))
		}
		if e.Line < 0 {
			problems = append(problems, fmt.Errorf("%w: %s has line %d", ErrMalformedEvidence, at, e.Line))
		}
	}
	return problems
}

// provenanceProblems checks a subject's origin and confidence are values the
// specification declares.
func provenanceProblems(o model.Origin, c model.Confidence, where string) []error {
	var problems []error
	if !o.Valid() {
		problems = append(problems, fmt.Errorf("%w: %s has origin %q", ErrUnknownOrigin, where, o))
	}
	if !c.Valid() {
		problems = append(problems, fmt.Errorf("%w: %s has confidence %q", ErrUnknownConfidence, where, c))
	}
	return problems
}
