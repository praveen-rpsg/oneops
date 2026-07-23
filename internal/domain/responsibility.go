package domain

import "context"

// ResponsibilityResult is the set of responsibility identifiers a Configuration
// Object owns. Responsibilities are opaque logical ids sourced only from
// Configuration Object metadata — never inferred from names. It is a pure domain
// value (no persistence, transport, or presentation fields).
type ResponsibilityResult struct {
	CfgID            string
	Responsibilities []string
}

// ReplacementResult reports whether a replacement owns every responsibility of
// the object it replaces (the owns(allResponsibilities) clause of the
// Replacement Test). It carries no governance or approval fields.
type ReplacementResult struct {
	OldCfgID    string
	NewCfgID    string
	Complete    bool
	Transferred []string // owned by both old and new
	Missing     []string // owned by old but not by new
}

// ResponsibilityGraph is the read-only data the ResponsibilityEvaluator consumes.
// It is satisfied by the store that composes the M2 graph accessors with a
// metadata-backed responsibility source. Ownership comes only from metadata.
type ResponsibilityGraph interface {
	// ObjectExists reports whether a Configuration Object with cfgID exists.
	ObjectExists(ctx context.Context, cfgID string) (bool, error)
	// EdgesTo returns the direct in-edges of cfgID (used to find superseders).
	EdgesTo(ctx context.Context, cfgID string) ([]*DependencyEdge, error)
	// Responsibilities returns the raw responsibility ids declared in cfgID's
	// metadata (order/duplicates as stored; the evaluator validates them).
	Responsibilities(ctx context.Context, cfgID string) ([]string, error)
}
