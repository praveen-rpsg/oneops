package domain

import "context"

// GapResult reports whether removing a Configuration Object would leave any of
// its declared operational coverage unowned by an ACTIVE configuration — the
// graph-independent noGapIfRemoved clause of the Replacement Test. Coverage ids
// are opaque logical identifiers sourced only from Configuration Object metadata:
// never inferred from names, scanned from source, read from documents, or probed
// from runtime systems. It is a pure domain value (no persistence, transport, or
// presentation fields).
type GapResult struct {
	CfgID  string
	HasGap bool
	// UncoveredCapabilities lists the coverage ids the object declares that no
	// other ACTIVE configuration provides, sorted and deduplicated. It is the
	// reason a superseded object remains Active.
	UncoveredCapabilities []string
}

// GapReplacementResult reports whether a specific replacement covers every
// operational coverage id of the object it replaces (the targeted form of
// noGapIfRemoved). It carries no governance, approval, or execution fields —
// those remain deferred.
type GapReplacementResult struct {
	OldCfgID string
	NewCfgID string
	// Complete is true when NewCfgID provides every coverage id of OldCfgID.
	Complete bool
	// Covered lists coverage ids provided by both old and new.
	Covered []string
	// Missing lists coverage ids the old provides but the new does not.
	Missing []string
}

// CoverageGraph is the read-only data the GapEvaluator consumes. It is satisfied
// by the store that composes the M2 graph accessors with a metadata-backed
// coverage source. Coverage comes only from metadata; the graph itself stays
// authority-agnostic and is never modified.
type CoverageGraph interface {
	// ObjectExists reports whether a Configuration Object with cfgID exists.
	ObjectExists(ctx context.Context, cfgID string) (bool, error)
	// Coverage returns the raw operational-coverage ids declared in cfgID's
	// `coverage` metadata (segments as stored — the evaluator trims, rejects empty
	// segments as malformed, and rejects duplicates). Coverage ids are opaque
	// logical identifiers, not object references. An object that declares no
	// coverage yields an empty slice.
	Coverage(ctx context.Context, cfgID string) ([]string, error)
}
