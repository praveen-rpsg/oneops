package domain

import (
	"context"
	"strings"
)

// Direction is the orientation of a graph traversal.
type Direction string

const (
	// DirectionDependencies walks forward: root → the things it depends on.
	DirectionDependencies Direction = "dependencies"
	// DirectionDependents walks reverse: root ← the things that depend on it.
	DirectionDependents Direction = "dependents"
)

// Valid reports whether d is a known direction.
func (d Direction) Valid() bool {
	switch d {
	case DirectionDependencies, DirectionDependents:
		return true
	default:
		return false
	}
}

// TraversalNode is a reachable node and its minimum depth from the root. It is a
// pure graph primitive — it carries no authority, lifecycle, or baseline state.
type TraversalNode struct {
	CfgID string
	Depth int
}

// TraversalResult is the deduplicated, deterministically-ordered set of nodes
// reached by a traversal. It carries no State Model semantics.
type TraversalResult struct {
	Root      string
	Direction Direction
	Nodes     []TraversalNode
}

// Count returns the number of distinct reachable nodes.
func (r *TraversalResult) Count() int { return len(r.Nodes) }

// MaxDepth returns the greatest depth in the result, or 0 when empty.
func (r *TraversalResult) MaxDepth() int {
	deepest := 0
	for _, n := range r.Nodes {
		if n.Depth > deepest {
			deepest = n.Depth
		}
	}
	return deepest
}

// IDs returns the reachable node ids in result order.
func (r *TraversalResult) IDs() []string {
	ids := make([]string, len(r.Nodes))
	for i, n := range r.Nodes {
		ids[i] = n.CfgID
	}
	return ids
}

// GraphPath is an ordered sequence of node ids.
type GraphPath struct {
	Nodes []string
}

// Len returns the number of nodes in the path.
func (p GraphPath) Len() int { return len(p.Nodes) }

// String renders the path as "a -> b -> c".
func (p GraphPath) String() string { return strings.Join(p.Nodes, " -> ") }

// Cycle is a detected cycle, expressed as a closed path (its first and last node
// are identical), e.g. a → b → c → a.
type Cycle struct {
	Path GraphPath
}

// GraphTraversal is the read-only traversal contract over the dependency graph.
// It performs traversal only: no mutation, no authority, no lifecycle semantics.
// Implementations must be safe for concurrent use.
type GraphTraversal interface {
	// Dependencies returns the direct (one-hop) dependency node ids of cfgID.
	Dependencies(ctx context.Context, cfgID string) ([]string, error)
	// Dependents returns the direct (one-hop) dependent node ids of cfgID.
	Dependents(ctx context.Context, cfgID string) ([]string, error)
	// RecursiveDependencies returns the transitive forward closure with depths,
	// deduplicated and cycle-safe.
	RecursiveDependencies(ctx context.Context, cfgID string) ([]TraversalNode, error)
	// RecursiveDependents returns the transitive reverse closure with depths,
	// deduplicated and cycle-safe.
	RecursiveDependents(ctx context.Context, cfgID string) ([]TraversalNode, error)
	// CyclePaths returns the closing paths of cycles reachable from cfgID in the
	// given direction. It backs cycle detection; it does not repair cycles.
	CyclePaths(ctx context.Context, cfgID string, dir Direction) ([]GraphPath, error)
}
