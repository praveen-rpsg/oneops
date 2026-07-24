package domain

// Ownership propagation for privileged workers.
//
// A privileged worker may read across every tenant — the event relay, the
// policy consumer and the integrity sweeper all must, because they serve the
// whole installation from one process and run on a connection that bypasses
// row-level security by design.
//
// Reading across tenants is permitted. Acting across tenants is not.
//
// That distinction was violated twice in the same shape. The relay matched
// audit events against webhook subscriptions on operation and resource, and the
// policy consumer matched them against policy conditions on operation,
// resource, actor and metadata. Neither compared tenant, so a subscription with
// no filters — the documented way to subscribe to everything — selected every
// other tenant's events. Verified against the running service: an attacker's
// endpoint received a victim's governance event, HMAC-signed with the
// attacker's own secret.
//
// No row-level policy can catch that. The worker's exemption is correct; the
// defect is acting on what it read without re-establishing whose data it was.
//
// This file exists so no worker decides *who* an action is for. A worker
// supplies a predicate that decides *what* to act on; ownership is enforced
// here, once.

// Owned is anything that belongs to exactly one tenant.
//
// Implementing it is the declaration that a type participates in tenant
// isolation. Types that reach a privileged worker's fan-out must implement it,
// which is what makes the ownership check impossible to reach around: FanOut
// will not accept a value that cannot state its owner.
type Owned interface {
	// OwnerTenantID returns the tenant this value belongs to. An empty string
	// means the owner is unknown, which is always treated as "matches nothing"
	// rather than "matches everything".
	OwnerTenantID() string
}

// SameOwner reports whether a and b belong to the same, known tenant.
//
// Empty is never equal to empty. A value whose owner is unknown fails closed,
// so a row written before ownership existed — or by a future path that forgets
// to populate it — is excluded rather than universally matched. The inverse
// default is what made the original defect total instead of partial.
func SameOwner(a, b Owned) bool {
	ao, bo := a.OwnerTenantID(), b.OwnerTenantID()
	return ao != "" && bo != "" && ao == bo
}

// FanOut returns the subscribers that may act on ev.
//
// It is the single place a privileged worker turns a cross-tenant read into
// per-subscriber work. Ownership is enforced before selects is consulted, so a
// worker's predicate cannot widen the tenant boundary however it is written —
// the predicate answers "is this the kind of thing I act on", never "is this
// mine to act on".
//
// A subscriber list read privileged spans every tenant; the returned slice
// never does.
func FanOut[S Owned](ev Owned, subs []S, selects func(S) bool) []S {
	if ev.OwnerTenantID() == "" {
		// An event of unknown ownership is delivered to nobody. Preferring
		// silence to a guess is the whole point: the alternative is delivering
		// one tenant's governance history to another's endpoint.
		return nil
	}
	var out []S
	for _, s := range subs {
		if SameOwner(s, ev) && selects(s) {
			out = append(out, s)
		}
	}
	return out
}
