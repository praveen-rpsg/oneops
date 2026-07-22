package domain

// AuditChainID returns the audit-chain identifier for a Configuration Object's
// constitutional operations.
//
// The audit chain is per governed object: a Configuration Object's ChainID is
// its own canonical Configuration Object id (CfgID). This is the single,
// authoritative mapping from a governed object to its audit chain — callers must
// obtain the ChainID from here rather than assuming or re-deriving the rule, so
// the mapping can evolve (e.g. to a tenant- or stream-scoped key) without any
// call site changing.
//
// The mapping is pure, total, and stable for the object's lifetime: every
// operation on a given object appends to the same chain, which is what makes the
// object's per-row serialization in the governance engine sufficient to order
// that chain's appends. AuditChainID performs no validation; an empty CfgID
// yields an empty ChainID, which the audit append path rejects downstream.
func AuditChainID(cfgID string) string {
	return cfgID
}
