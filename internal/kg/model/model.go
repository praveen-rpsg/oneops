// Package model holds the shared vocabulary of the Platform Knowledge Graph:
// the values a fact's provenance and trustworthiness may take.
//
// It is the sink of the knowledge-graph dependency DAG (Amendment A3): it
// imports nothing, and no package under internal/kg is beneath it. The topology
// that uses this vocabulary lives in the graph package, which depends on this
// one and never the reverse.
//
// Origin and Confidence are independent (specification §V). Origin says where a
// fact came from; Confidence says how much it can be trusted. The rule binding
// them — an inferred fact may never be certain — belongs to the confidence
// scorer, not here: this package answers only whether a value is one the
// specification declares.
package model

// Origin is where a fact came from (specification §II).
type Origin string

// The origins the specification declares. No other value is valid.
const (
	// OriginDerived is a fact read from an executable artifact.
	OriginDerived Origin = "derived"
	// OriginDeclared is a fact a human wrote into .pkg/, versioned in the repo.
	OriginDeclared Origin = "declared"
	// OriginImported is a fact taken from a structured document.
	OriginImported Origin = "imported"
	// OriginInferred is a fact obtained by a heuristic over prose.
	OriginInferred Origin = "inferred"
	// OriginRatified is a fact stated by an ADR header.
	OriginRatified Origin = "ratified"
)

// Valid reports whether o is one of the declared origins.
func (o Origin) Valid() bool {
	switch o {
	case OriginDerived, OriginDeclared, OriginImported, OriginInferred, OriginRatified:
		return true
	}
	return false
}

// Confidence is how far a fact may be trusted (specification §II).
type Confidence string

// The confidences the specification declares. No other value is valid.
const (
	// ConfidenceCertain is deterministic from an executable artifact.
	ConfidenceCertain Confidence = "certain"
	// ConfidenceHigh is structured text.
	ConfidenceHigh Confidence = "high"
	// ConfidenceMedium is a heuristic over prose, and must be labelled as such.
	ConfidenceMedium Confidence = "medium"
	// ConfidenceLow is weaker than medium and advisory only.
	ConfidenceLow Confidence = "low"
	// ConfidenceUnknown is unscored.
	ConfidenceUnknown Confidence = "unknown"
)

// Valid reports whether c is one of the declared confidences.
func (c Confidence) Valid() bool {
	switch c {
	case ConfidenceCertain, ConfidenceHigh, ConfidenceMedium, ConfidenceLow, ConfidenceUnknown:
		return true
	}
	return false
}
