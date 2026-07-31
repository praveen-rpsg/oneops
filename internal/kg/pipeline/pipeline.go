// Package pipeline composes the extractors into a single derived graph.
//
// It owns the stages §IV places between extraction and serialisation: run each
// extractor, merge what they produce, put the collections into canonical order,
// and refuse anything that does not satisfy §II. It does not serialise. §I
// places storage below the pipeline, so writing pkg.json belongs to cmd/kg,
// which may import both.
//
// The pipeline orchestrates; it does not reinterpret. Every fact it emits comes
// from an extractor, and every rule it enforces is graph.Validate's. If a
// derived graph is rejected, the defect is upstream and the pipeline's job is to
// stop rather than to repair.
//
// Freshness is Graph.Commit, read from git (ADR-PKG-001, Amendment A3 §C3). A
// graph that cannot state which tree produced it cannot be checked for
// staleness, so a missing commit is fatal rather than blank.
package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"github.com/rpsg/oneops/internal/kg/extract/gopkg"
	"github.com/rpsg/oneops/internal/kg/graph"
)

// The failures a build declares.
var (
	// ErrNoExtractor is a build with nothing registered to derive from. It
	// would otherwise succeed and emit an empty graph, which is
	// indistinguishable from a repository that really has no packages.
	ErrNoExtractor = errors.New("pipeline: no extractor was registered")
	// ErrExtract is an extractor that failed. §III makes this fatal for E1,
	// and a partial graph is worse than none: consumers cannot tell a missing
	// subtree from a subtree that does not exist.
	ErrExtract = errors.New("pipeline: extractor failed")
	// ErrCommit is a repository whose HEAD cannot be read.
	ErrCommit = errors.New("pipeline: cannot read the repository commit")
	// ErrInvalidGraph is a derived graph that violates §II.
	ErrInvalidGraph = errors.New("pipeline: the derived graph is not valid")
)

// Extractor is the contract §III defines.
//
// Declared here, where it is consumed, because §I places the canonical
// interface and registry under extract/ and no story has created that package
// yet. Go's structural typing means an extract/ declaration will match this one
// exactly, so nothing has to change when it arrives.
type Extractor interface {
	ID() string
	Extract(ctx context.Context, root string) ([]graph.Node, []graph.Edge, error)
}

// Default is the registered extractor set.
//
// One entry today. §III lists E1 through E11; each arrives with its own story
// and is added here.
func Default() []Extractor {
	return []Extractor{gopkg.Extractor{}}
}

// Build derives the knowledge graph for the repository rooted at root.
func Build(ctx context.Context, root string) (*graph.Graph, error) {
	return BuildWith(ctx, root, Default())
}

// BuildWith derives the graph using a caller-supplied extractor set.
//
// Exported so a caller can build a graph from a known set rather than the
// registry — which is how the failure paths are exercised, since a real
// extractor cannot be made to fail on demand.
func BuildWith(ctx context.Context, root string, extractors []Extractor) (*graph.Graph, error) {
	if len(extractors) == 0 {
		return nil, ErrNoExtractor
	}

	commit, err := headCommit(ctx, root)
	if err != nil {
		return nil, err
	}

	var nodes []graph.Node
	var edges []graph.Edge
	for _, e := range extractors {
		n, ed, xerr := e.Extract(ctx, root)
		if xerr != nil {
			return nil, fmt.Errorf("%w [%s]: %w", ErrExtract, e.ID(), xerr)
		}
		nodes = append(nodes, n...)
		edges = append(edges, ed...)
	}

	// Normalise (§IV). Each extractor sorts what it returns, but concatenating
	// two sorted runs does not give a sorted whole, so canonical order is
	// re-established over the merged result. graph.Validate reports disorder
	// rather than repairing it, and this is the producer it holds responsible.
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

	g := &graph.Graph{
		SchemaVersion: graph.SchemaVersion,
		Commit:        commit,
		Nodes:         nodes,
		Edges:         edges,
	}

	// Mandatory, and deliberately last: a graph that fails §II is never
	// returned, so nothing downstream has to ask whether it was checked.
	if verr := g.Validate(); verr != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidGraph, verr)
	}
	return g, nil
}

// headCommit reads the commit the derived graph describes.
func headCommit(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w [root %s]: %v: %s",
			ErrCommit, root, err, strings.TrimSpace(stderr.String()))
	}
	commit := strings.TrimSpace(stdout.String())
	if commit == "" {
		return "", fmt.Errorf("%w [root %s]: git reported an empty HEAD", ErrCommit, root)
	}
	return commit, nil
}
