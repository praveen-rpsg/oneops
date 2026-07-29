# ADR-TENANCY-005 — Replay owns no authority

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-25 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-003 (execution ownership is re-derived), ADR-TENANCY-004 (authoritative ownership must be consistent) |

## Context

Replay re-runs historical work: the time-window worker re-reads committed audit
events and re-enqueues deliveries for a webhook; the delivery-id worker requeues
existing delivery rows. The threat model for this phase treats replay as
attacker-controlled — historical work from a partial restore, a resurrected
queue, manual replay tooling, or a failed recovery must not be able to weaken
ownership guarantees.

The question is whether any replay path can cause an outbound action that was
not authorised against current authoritative ownership.

## Investigation

Attacked against the running service, three ways:

1. **Cross-tenant time-window replay.** An attacker registered a webhook and
   requested a wide-window replay covering a victim's events. The job completed
   with `events_replayed: 0` and the attacker's endpoint received nothing: the
   producer-side owner filter excluded the victim's events, and nothing was even
   enqueued.

2. **Split-brain replay.** With a split-brain audit row injected at runtime (a
   victim's chain, an attacker's tenant label — the ADR-TENANCY-004 corruption),
   the attacker replayed again. The producer filter, fooled by the label, this
   time enqueued a delivery — and the dispatcher refused it: ownership re-derived
   from the governed object was the victim, the webhook was the attacker's, so
   the delivery was dead-lettered. Zero exfiltration.

3. **Legitimate replay** of a tenant's own events to its own webhook still
   delivered, confirming the refusals above are not blanket failure.

The investigation also surfaced a real defect: `webhook_replay_job.CreateJob`
was left without a `tenant_id` in the row-level-security rollout, so under the
tenant-scoped pool every non-system tenant's `POST .../replay` failed the
`WITH CHECK` policy and returned HTTP 500. Replay was broken for real customers.
Fixed by writing ownership on create, and the job is now tenant-isolated.

## Decision

**Replay owns no ownership authority, because replay performs no outbound
action.** It is a producer: it enqueues or requeues, and every outbound send is
performed by the dispatcher, which re-derives ownership through
`domain.ResolveAndAuthorize` (ADR-TENANCY-003/004). A replayed delivery is
authorised at execution exactly as a freshly produced one is — historical
correctness is never sufficient, current authoritative state always decides.

This is now structural, not incidental:

- The replay worker is constructed with no HTTP client and holds no outbound
  capability. Its producer-side owner filter is defence in depth, not the
  guarantee; the guarantee is the dispatcher's re-derivation.
- An architecture test fails the build if the replay worker — or any producer,
  including the relay and the policy consumer — imports `net/http` or calls a
  client's `Do`. A producer that could send would bypass the authoritative check,
  and this keeps replay a producer by construction rather than by discipline.

## Consequences

**Worker restart and duplicate replay are safe by the same property.** Replay
holds no cached authority: ownership is re-derived from the database at every
dispatch, so a crash mid-replay, a restart, or a re-run of the same window
changes nothing about authorisation. Duplicate replay can produce duplicate
deliveries — an at-least-once delivery property, not an ownership one — and each
is authorised identically.

**Partial restore fails closed through the existing layers.** A restore that
brings back audit rows without their governed object yields chains the dispatcher
cannot resolve (`ErrEventNotFound`); one that reintroduces divergent ownership is
refused at execution and refuses startup (ADR-TENANCY-004). Replay adds no new
path around either.

**Residual risk.** Replay's guarantee is inherited from the dispatcher, so it is
exactly as strong as ADR-TENANCY-004 and no stronger: an attacker who can forge
both the governed object and the audit log consistently still owns the data by
definition. What replay cannot do is manufacture an outbound action that skips
that check.

## The invariant

A replay path is a producer. It supplies historical coordinates and nothing
else; it establishes no ownership and performs no outbound action. Trust is
established once, at execution, by the authoritative resolver — and replayed work
is authorised against current authoritative state, never against the state that
was true when the work was first produced.
