package domain

import (
	"context"
	"strings"
	"time"
)

// EdgeKind is the relationship a dependency edge expresses between two
// Configuration Objects. The set is closed (Configuration State Model §6/§8):
// depends and extends are dependency-carrying; supersedes records replacement.
// Extending the set is a constitutional Amendment, never a code change.
type EdgeKind string

const (
	// EdgeKindDepends: the source depends on the target.
	EdgeKindDepends EdgeKind = "depends"
	// EdgeKindExtends: the source extends the target (extension is not replacement).
	EdgeKindExtends EdgeKind = "extends"
	// EdgeKindSupersedes: the source supersedes (replaces) the target.
	EdgeKindSupersedes EdgeKind = "supersedes"
)

// Valid reports whether k is a known edge kind. Unknown values are rejected.
func (k EdgeKind) Valid() bool {
	switch k {
	case EdgeKindDepends, EdgeKindExtends, EdgeKindSupersedes:
		return true
	default:
		return false
	}
}

// DependencyEdge is a directed edge (FromCfg → ToCfg) of a given EdgeKind
// between two Configuration Objects. It is the storage-layer entity for the
// dependency graph; traversal and authority semantics belong to later stories.
type DependencyEdge struct {
	ID        string
	FromCfg   string
	ToCfg     string
	EdgeKind  EdgeKind
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate enforces schema-level invariants: both endpoints are present and the
// edge kind is known. It carries no business rules beyond this. It returns a
// *ValidationError on the first violation, or nil when well-formed.
func (e *DependencyEdge) Validate() error {
	if strings.TrimSpace(e.FromCfg) == "" {
		return newValidation("from_cfg", "must not be empty")
	}
	if strings.TrimSpace(e.ToCfg) == "" {
		return newValidation("to_cfg", "must not be empty")
	}
	if !e.EdgeKind.Valid() {
		return newValidation("edge_kind", "unknown edge kind: "+string(e.EdgeKind))
	}
	return nil
}

// GraphRepository is the persistence contract for DependencyEdge. It is a pure
// storage interface — no traversal, recursive queries, or authority logic.
// Implementations must be safe for concurrent use.
type GraphRepository interface {
	// CreateEdge inserts an edge. Returns ErrConflict on a duplicate
	// (from,to,kind), ErrNotFound when an endpoint object does not exist, or a
	// *ValidationError when the edge is malformed.
	CreateEdge(ctx context.Context, edge *DependencyEdge) (*DependencyEdge, error)
	// DeleteEdge removes an edge by id, or returns ErrNotFound.
	DeleteEdge(ctx context.Context, id string) error
	// Edge returns an edge by id, or ErrNotFound.
	Edge(ctx context.Context, id string) (*DependencyEdge, error)
	// EdgesFrom returns the direct out-edges of fromCfg (no traversal).
	EdgesFrom(ctx context.Context, fromCfg string) ([]*DependencyEdge, error)
	// EdgesTo returns the direct in-edges of toCfg (no traversal).
	EdgesTo(ctx context.Context, toCfg string) ([]*DependencyEdge, error)
	// Exists reports whether an edge (fromCfg,toCfg,kind) is present.
	Exists(ctx context.Context, fromCfg, toCfg string, kind EdgeKind) (bool, error)
	// List returns up to limit edges ordered by (created_at, id).
	List(ctx context.Context, limit int) ([]*DependencyEdge, error)
}
