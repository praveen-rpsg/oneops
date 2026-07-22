package domain

import "context"

// ActiveCitationResult reports whether a Configuration Object is still cited by
// an ACTIVE artifact — the graph-independent noActiveArtifactCites clause of the
// Replacement Test. Citations are opaque logical identifiers sourced only from
// Configuration Object metadata: never inferred from names, scanned from source,
// or read from document bodies. It is a pure domain value (no persistence,
// transport, or presentation fields).
type ActiveCitationResult struct {
	CfgID             string
	HasActiveCitation bool
	// CitingArtifacts lists the ACTIVE artifacts whose metadata cites CfgID,
	// sorted and deduplicated. It is the reason a superseded object remains Active.
	CitingArtifacts []string
}

// CitationReplacementResult reports whether a replacement clears the active
// citations to the object it replaces: an ACTIVE artifact that still cites the
// old object blocks the replacement, because that citation must be re-pointed at
// the new object before the old one may fall to Historical. It carries no
// governance, approval, or operational-gap fields — those remain deferred.
type CitationReplacementResult struct {
	OldCfgID string
	NewCfgID string
	// Cleared is true when no ACTIVE artifact still cites OldCfgID.
	Cleared bool
	// Remaining lists the ACTIVE artifacts that still cite OldCfgID.
	Remaining []string
}

// CitationGraph is the read-only data the ArtifactCitationEvaluator consumes. It
// is satisfied by the store that composes the M2 graph accessors with a
// metadata-backed citation source. Citations come only from metadata; the graph
// itself stays authority-agnostic and is never modified.
type CitationGraph interface {
	// ObjectExists reports whether a Configuration Object with cfgID exists.
	ObjectExists(ctx context.Context, cfgID string) (bool, error)
	// Citations returns the raw citation ids declared in cfgID's `citations`
	// metadata (segments as stored — the evaluator trims, rejects empty segments
	// as malformed, rejects duplicates, and rejects references to unknown
	// objects). An object that declares no citations yields an empty slice.
	Citations(ctx context.Context, cfgID string) ([]string, error)
}
