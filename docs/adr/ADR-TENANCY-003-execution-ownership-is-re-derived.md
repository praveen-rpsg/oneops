# ADR-TENANCY-003 — Execution-time ownership is re-derived, never trusted

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-25 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-001/002 (isolation, wiring), ADR-AUDIT-005 (atomic constitutional mutation) |

## Context

ADR-TENANCY-002 established that a privileged worker may read across tenants but
must not act across them, and moved producer-side ownership into a shared
fan-out framework. That confined the queues the relay and policy consumer
*produce*.

It did not defend the *consumer*. A queue is storage, and storage is untrusted:
a delivery row may predate ownership, arrive from a restore, be written by
replay tooling, or be inserted by anyone with database access. The dispatcher
fetched a webhook by id and delivered, comparing nothing.

A first fix added `AuthorizeExecution(webhook, delivery)`, comparing the
delivery row's owner label against the webhook. Verified against the running
service, it refused a row whose two ownership fields disagreed — and delivered a
row forged self-consistently: label naming the attacker, matching the
attacker's webhook, while the event content belonged to the victim. Comparing
two fields of the same forged row proves nothing. The label is a claim, and the
consumer was authorising a claim against itself.

## Decision

**Ownership presented at execution time is a claim. The consumer re-derives the
fact.**

Before any outbound action a privileged consumer resolves the work's owner from
the authoritative record, not from the queue row, and authorises against that.

For webhook delivery the authoritative record is `audit_event.tenant_id` for the
event's `(chain_id, seq)`. It is authoritative because the audit log is
append-only and its `tenant_id` is written inside the governance transaction
(ADR-AUDIT-005): it is the one owner an attacker cannot rewrite after the event
is committed. `EventOwnerResolver.ResolveEventOwner` reads it; the dispatcher
compares the resolved owner against the subscription and never consults the
delivery's own label.

The pipeline is: **authoritative source → resolve owner → authorise → act.**
Queue metadata is advisory; authoritative state is mandatory.

Every failure is closed. An event absent from the append-only log
(`ErrEventNotFound`) will never appear, so its delivery is dead-lettered, not
retried. A resolver that is unavailable refuses rather than delivers. A
dispatcher constructed without a resolver refuses everything — the dependency is
required, not optional.

## Consequences

**The resolver reads on the privileged pool, by design.** The dispatcher serves
every tenant, and this read is the security *source*, not tenant data returned
to a caller. It reads a single column and compares it internally; it never
returns another tenant's data anywhere.

**The realistic residual is stated plainly.** This defeats a forged queue row
because the queue is no longer trusted. It does not defend against an attacker
who can forge the authoritative record itself — but `audit_event` refuses
UPDATE, DELETE and TRUNCATE (row and partition, per the audit hardening), so
rewriting a committed event's owner is not available even with database access;
only appending a *new* event is, and a new event has its own correct owner. The
boundary rests on the append-only guarantee, which is independently enforced and
independently tested.

**This generalises to every privileged consumer, and now does.** The webhook
dispatcher and the policy executor both perform outbound actions from queued
items, and both now reach ownership only through one shared function,
`domain.ResolveAndAuthorize`: the worker supplies the authoritative target (a
subscription or policy fetched by id) and the triggering event's coordinates,
and the function re-derives the event's owner from the audit log and refuses a
mismatch. No worker compares ownership itself, and neither reads the queued
item's owner label.

The policy executor was converted after being exploited live: a synthetic
execution pairing an attacker's HTTP-action policy with a victim's event
POSTed the victim's governance event to the attacker's endpoint
(`status: succeeded`, event captured). After conversion the same injection is
refused (`dead_letter`, target and work tenants logged as different, zero
exfiltration), while a policy triggered by its own tenant's event still runs.

**Update (2026-07-25).** The resolver contract, `ErrEventNotFound`, and
`ResolveAndAuthorize` moved into `domain` so there is one execution-security
framework rather than one per package. The dispatcher was re-routed through it
and re-verified — its forged-row refusal still holds live.

**Enforced mechanically.** `TestDispatcher_ForgedSelfConsistentRowIsRefused`
fails if the forged row is delivered. An architecture test fails if the
dispatcher stops calling `ResolveEventOwner`, or passes the delivery row into
`AuthorizeExecution` instead of the resolved owner. Both were verified by
reintroducing the label-trusting check as compiling code and confirming they
fail — one of them naming the exact line.

## The invariant

For a privileged worker, ownership carried alongside work is advisory. Ownership
is established by re-deriving it from the authoritative, append-only record
immediately before the side effect. A consumer that authorises a claim against
another part of the same claim has verified nothing.
