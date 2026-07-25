package domain

import (
	"context"
	"errors"
	"fmt"
)

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

// ErrOwnershipMismatch is returned when a privileged worker is asked to act on
// work that does not belong to the target of the action.
var ErrOwnershipMismatch = errors.New("execution refused: work and target belong to different tenants")

// AuthorizeExecution is the check a privileged worker performs immediately
// before an outbound side effect.
//
// Producer-side ownership is not sufficient. A queue is storage, and storage is
// untrusted: a row may have been written by an older producer that predates
// ownership, by a future regression, by replay tooling, by a restore from
// backup, or by an operator with database access. The consumer has to defend
// itself.
//
// Verified against the running service: a delivery row inserted directly into
// webhook_delivery, pairing one tenant's audit event with another tenant's
// webhook, was dispatched and marked delivered. The producer had correctly
// refused to create that pairing moments earlier — and it made no difference,
// because the dispatcher never asked.
//
// The guarantee this provides, stated precisely: work whose owner is unknown or
// differs from the action's target is refused. It does not detect a forged row
// that is internally self-consistent — one naming the same tenant on both sides
// while carrying another tenant's content — because distinguishing that
// requires re-reading the originating record, and an attacker able to write
// such a row already holds database write access. The boundary defended here is
// the realistic one: producer bugs, partial restores, and rows predating
// ownership, all of which fail closed.
func AuthorizeExecution(target, work Owned) error {
	if !SameOwner(target, work) {
		return fmt.Errorf("%w: target=%q work=%q",
			ErrOwnershipMismatch, target.OwnerTenantID(), work.OwnerTenantID())
	}
	return nil
}

// EventOwnerResolver returns the authoritative tenant that owns a committed
// event, read from the append-only audit log rather than from any queue row.
// It is the source of truth for execution-time ownership (ADR-TENANCY-003).
type EventOwnerResolver interface {
	ResolveEventOwner(ctx context.Context, chainID string, seq int64) (string, error)
}

// ErrEventNotFound means no committed event matches the chain and sequence.
// Execution treats it as a refusal, not a transient error: an event absent from
// an append-only log will never appear.
var ErrEventNotFound = errors.New("no committed event for chain and sequence")

// ResolveAndAuthorize is the one execution-security check every privileged
// consumer performs before an outbound action. The worker supplies the
// authoritative target (a policy or subscription fetched by id, not from the
// queue) and the coordinates of the triggering event; this re-derives the
// event's owner from the authoritative source and refuses unless the two match.
//
// No worker compares ownership itself. The dispatcher and the policy executor
// both reach ownership only through here, so a forged queue label cannot
// influence either — the label is never read. A resolver error, including
// ErrEventNotFound, propagates and the caller fails closed.
func ResolveAndAuthorize(ctx context.Context, r EventOwnerResolver, target Owned, chainID string, seq int64) error {
	owner, err := r.ResolveEventOwner(ctx, chainID, seq)
	if err != nil {
		return err
	}
	return AuthorizeExecution(target, ownerString(owner))
}

// ownerString adapts a re-derived owner id to Owned, so AuthorizeExecution
// compares against the authoritative tenant and never against a queue label.
type ownerString string

func (o ownerString) OwnerTenantID() string { return string(o) }
