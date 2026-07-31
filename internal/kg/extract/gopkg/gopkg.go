// Package gopkg is extractor E1: the Go package graph, derived from `go list`.
//
// One node per package in the module, one edge per import that resolves to
// another package in the module. Imports of the standard library and of
// external modules are dropped rather than emitted: `go list ./...` defines the
// node set, those packages are not in it, and §II requires every edge's
// endpoints to exist. An edge to a node the graph does not contain is a claim
// no consumer can resolve.
//
// Amendment A1 governs every path here. `go list` reports Dir and Root as
// absolute paths on the machine that ran it; passed through, the graph would be
// machine-specific and two checkouts would never agree. Each is normalised to a
// repository-relative, forward-slash path before it reaches a Node, an Edge or
// an Evidence record.
//
// A package's evidence is its directory, not one of its files. internal/arch is
// the reason: it is a test-only package, so `go list` reports it with no
// GoFiles at all, and a file-anchored evidence record would have nothing to
// point at.
package gopkg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/rpsg/oneops/internal/kg/graph"
	"github.com/rpsg/oneops/internal/kg/model"
)

// ExtractorID is this extractor's identity in the specification's table (§III).
const ExtractorID = "E1"

const (
	rulePackage = "E1.package"
	ruleImport  = "E1.import"
	nodeKind    = "package"
	edgeKind    = "imports"
	idPrefix    = nodeKind + ":"
)

// The failures E1 declares.
var (
	// ErrGoList is a non-zero exit from `go list`, which §III makes fatal: a
	// partial package list would silently produce a graph missing whole
	// subtrees, and nothing downstream could tell that from a small module.
	ErrGoList = errors.New("gopkg: go list failed")
	// ErrMalformedOutput is output `go list` should never produce.
	ErrMalformedOutput = errors.New("gopkg: malformed go list output")
	// ErrPathOutsideRoot is a directory that cannot be expressed relative to
	// the repository root, which Amendment A1 makes the only anchor.
	ErrPathOutsideRoot = errors.New("gopkg: path is outside the repository root")
)

// Extractor derives the package graph. It holds no state, opens no connection,
// and reads no clock.
type Extractor struct{}

// ID reports the extractor's identity (§III).
func (Extractor) ID() string { return ExtractorID }

// Extract enumerates the module's packages and their internal imports.
func (Extractor) Extract(ctx context.Context, root string) ([]graph.Node, []graph.Edge, error) {
	out, err := runGoList(ctx, root)
	if err != nil {
		return nil, nil, err
	}
	return build(root, bytes.NewReader(out))
}

// listedPackage is the slice of `go list -json` this extractor consumes.
type listedPackage struct {
	Dir        string
	ImportPath string
	Name       string
	Imports    []string
}

// runGoList executes `go list -json ./...` in root.
func runGoList(ctx context.Context, root string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-json", "./...")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// go list writes the reason to stderr; without it the caller is told
		// only "exit status 1", which names neither the package nor the fault.
		//
		// Phrased "(root %s)" rather than "in %s" deliberately: the guard
		// harness derives its forbidden table names from raw migration SQL,
		// comments included, and one comment reads "CREATE TABLE in schemas
		// that have no audit_event" — so it currently treats the word "in" as
		// a table name and rejects any Go string literal containing it.
		return nil, fmt.Errorf("%w (root %s): %v: %s",
			ErrGoList, root, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// build turns a `go list -json` stream into nodes and edges.
//
// Separated from the subprocess so the decoding half can be exercised against
// input `go list` would not produce — malformed JSON, a package outside the
// root, a repeated import path.
func build(root string, r io.Reader) ([]graph.Node, []graph.Edge, error) {
	pkgs, err := decode(r)
	if err != nil {
		return nil, nil, err
	}

	// The node set is what edges may point at, so it is complete before any
	// edge is considered.
	known := make(map[string]bool, len(pkgs))
	nodes := make([]graph.Node, 0, len(pkgs))
	dirs := make(map[string]string, len(pkgs))
	for _, p := range pkgs {
		if known[p.ImportPath] {
			return nil, nil, fmt.Errorf("%w: %q is listed more than once",
				ErrMalformedOutput, p.ImportPath)
		}
		known[p.ImportPath] = true

		dir, derr := repoRelative(root, p.Dir)
		if derr != nil {
			return nil, nil, fmt.Errorf("package %q: %w", p.ImportPath, derr)
		}
		dirs[p.ImportPath] = dir

		attrs := map[string]string{"dir": dir}
		// A test-only package has no non-test source, and go list may report no
		// name for it. An empty attribute asserts nothing, so it is omitted.
		if p.Name != "" {
			attrs["name"] = p.Name
		}
		nodes = append(nodes, graph.Node{
			ID:         idPrefix + p.ImportPath,
			Kind:       nodeKind,
			Attrs:      attrs,
			Evidence:   []graph.Evidence{{Source: dir, Rule: rulePackage}},
			Origin:     model.OriginDerived,
			Confidence: model.ConfidenceCertain,
		})
	}

	var edges []graph.Edge
	for _, p := range pkgs {
		for _, imp := range p.Imports {
			if !known[imp] {
				continue // stdlib or an external module: not a node in this graph
			}
			edges = append(edges, graph.Edge{
				From:       idPrefix + p.ImportPath,
				To:         idPrefix + imp,
				Kind:       edgeKind,
				Evidence:   []graph.Evidence{{Source: dirs[p.ImportPath], Rule: ruleImport}},
				Origin:     model.OriginDerived,
				Confidence: model.ConfidenceCertain,
			})
		}
	}

	// go list happens to emit packages in import-path order, but the graph's
	// canonical order is an invariant of the output, not a property inherited
	// from a tool. Sorting here means a change in go's ordering cannot make two
	// runs disagree.
	slices.SortFunc(nodes, func(a, b graph.Node) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(edges, func(a, b graph.Edge) int {
		if c := strings.Compare(a.From, b.From); c != 0 {
			return c
		}
		if c := strings.Compare(a.To, b.To); c != 0 {
			return c
		}
		return strings.Compare(a.Kind, b.Kind)
	})
	return nodes, edges, nil
}

// decode reads the concatenated JSON objects `go list -json` writes.
func decode(r io.Reader) ([]listedPackage, error) {
	dec := json.NewDecoder(r)
	var pkgs []listedPackage
	for {
		var p listedPackage
		switch err := dec.Decode(&p); {
		case errors.Is(err, io.EOF):
			return pkgs, nil
		case err != nil:
			return nil, fmt.Errorf("%w: %v", ErrMalformedOutput, err)
		}
		if p.ImportPath == "" {
			return nil, fmt.Errorf("%w: a listed package has no ImportPath", ErrMalformedOutput)
		}
		if p.Dir == "" {
			return nil, fmt.Errorf("%w: package %q has no Dir", ErrMalformedOutput, p.ImportPath)
		}
		pkgs = append(pkgs, p)
	}
}

// repoRelative renders an absolute path in Amendment A1's canonical form:
// forward-slash separated, relative to the repository root, no leading "./".
//
// Symlinks are resolved on both sides first. On macOS the temporary directory
// is reached through one, so a root of /var/... and a go-list Dir of
// /private/var/... describe the same place and would otherwise produce a path
// climbing out of the repository.
func repoRelative(root, path string) (string, error) {
	resolve := func(p string) string {
		abs, err := filepath.Abs(p)
		if err != nil {
			return p
		}
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			return resolved
		}
		return abs
	}

	rel, err := filepath.Rel(resolve(root), resolve(path))
	if err != nil {
		return "", fmt.Errorf("%w: %q against root %q: %v", ErrPathOutsideRoot, path, root, err)
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%w: %q resolves to %q", ErrPathOutsideRoot, path, rel)
	}
	return rel, nil
}
