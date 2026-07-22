package domain

import "context"

// AuthorityState is the computed constitutional authority of a Configuration
// Object (Configuration State Model §2). It is always computed from the
// dependency graph, supersession, and baseline membership — never stored,
// hardcoded, or inferred from naming.
type AuthorityState string

const (
	// AuthorityStateActive: in the in-force baseline, or transitively required by it.
	AuthorityStateActive AuthorityState = "ACTIVE"
	// AuthorityStateHistorical: superseded (it once governed, now replaced).
	AuthorityStateHistorical AuthorityState = "HISTORICAL"
	// AuthorityStateNonNormative: exists but governs nothing.
	AuthorityStateNonNormative AuthorityState = "NON_NORMATIVE"
	// AuthorityStateUnknown: authority cannot be determined.
	AuthorityStateUnknown AuthorityState = "UNKNOWN"
)

// Valid reports whether s is a known authority state.
func (s AuthorityState) Valid() bool {
	switch s {
	case AuthorityStateActive, AuthorityStateHistorical, AuthorityStateNonNormative, AuthorityStateUnknown:
		return true
	default:
		return false
	}
}

// AuthorityReason is the rule that determined the authority state.
type AuthorityReason string

const (
	// ReasonBaselineMember: the object is a current-baseline member.
	ReasonBaselineMember AuthorityReason = "baseline_member"
	// ReasonReachableFromBaseline: transitively required (depends/extends) by a baseline member.
	ReasonReachableFromBaseline AuthorityReason = "reachable_from_baseline"
	// ReasonSuperseded: the object has an incoming supersedes edge.
	ReasonSuperseded AuthorityReason = "superseded"
	// ReasonNotReachable: the object exists but no baseline reaches it.
	ReasonNotReachable AuthorityReason = "not_reachable_from_baseline"
	// ReasonObjectNotFound: no Configuration Object with this id exists.
	ReasonObjectNotFound AuthorityReason = "object_not_found"
	// ReasonInconsistentBaselineSuperseded: a baseline member is also superseded (contradiction).
	ReasonInconsistentBaselineSuperseded AuthorityReason = "inconsistent_baseline_superseded"
	// ReasonSupersededActiveDependency (M3.2): superseded but still required by an Active
	// configuration, so it remains Active (graph-computable Replacement-Test clause).
	ReasonSupersededActiveDependency AuthorityReason = "superseded_active_dependency"
	// ReasonReplacementIncomplete (M3.3): superseded, but the replacement does not own
	// every responsibility, so it remains Active (owns-all-responsibilities clause).
	ReasonReplacementIncomplete AuthorityReason = "replacement_incomplete"
)

// AuthorityEvidence explains WHY an authority decision was made. It is a domain
// value (no presentation fields, no transport DTO).
type AuthorityEvidence struct {
	// BaselineMember is true when the object is itself a current-baseline member.
	BaselineMember bool
	// ReachableVia names the immediate predecessor through which an Active baseline
	// reaches this object (empty for baseline members).
	ReachableVia string
	// SupersededBy lists the cfg_ids whose supersedes edges target this object.
	SupersededBy []string
	// ActiveDependents (M3.2) lists the Active configurations that still depend on
	// this object — the reason a superseded object remains Active.
	ActiveDependents []string
	// MissingResponsibilities (M3.3) lists the responsibility ids the replacement
	// does not own — the reason a superseded object remains Active.
	MissingResponsibilities []string
	// Notes is a short human-readable rationale.
	Notes string
}

// ActiveDependencyResult reports whether an object is still required by an Active
// configuration (the graph-computable Replacement-Test clause). It is a pure
// domain value — no persistence, transport, or presentation fields.
type ActiveDependencyResult struct {
	CfgID               string
	HasActiveDependency bool
	ActiveDependents    []string
}

// AuthorityResult is the computed authority of a single object with its evidence.
type AuthorityResult struct {
	CfgID    string
	State    AuthorityState
	Reason   AuthorityReason
	Evidence AuthorityEvidence
}

// AuthorityGraph is the read-only data the AuthorityResolver consumes. It is
// satisfied by composing the M2 graph repository/traversal with a baseline
// provider. Authority logic lives entirely in the resolver — the graph never
// understands authority.
type AuthorityGraph interface {
	// BaselineRoots returns the cfg_ids of current-baseline members, ordered
	// deterministically.
	BaselineRoots(ctx context.Context) ([]string, error)
	// ObjectExists reports whether a Configuration Object with cfgID exists.
	ObjectExists(ctx context.Context, cfgID string) (bool, error)
	// EdgesFrom returns the direct out-edges of cfgID (kind-aware; M2.1).
	EdgesFrom(ctx context.Context, cfgID string) ([]*DependencyEdge, error)
	// EdgesTo returns the direct in-edges of cfgID (kind-aware; M2.1).
	EdgesTo(ctx context.Context, cfgID string) ([]*DependencyEdge, error)
	// RecursiveDependencies returns the transitive forward closure (M2.2).
	RecursiveDependencies(ctx context.Context, cfgID string) ([]TraversalNode, error)
}
