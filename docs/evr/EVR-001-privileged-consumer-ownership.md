# EVR-001 — Privileged-consumer ownership re-derivation

| | |
|---|---|
| **Date** | 2026-07-29 |
| **Auditor** | Acting CTO / Evidence Authority |
| **Trust Register entries** | 4, 5, 6, 7 |
| **Associated ADR** | ADR-TENANCY-003 (execution ownership is re-derived), ADR-TENANCY-004 (authoritative ownership must be consistent), ADR-TENANCY-005 (replay owns no authority) |
| **Confidence** | **PARTIALLY VALIDATED** |

## Original claim

> Ownership presented at execution time is a claim. The consumer re-derives the
> fact. […] The pipeline is: authoritative source → resolve owner → authorise →
> act. Queue metadata is advisory; authoritative state is mandatory.

Recorded in the Trust Register as four eliminated classes: cross-tenant relay
(4), privileged-worker ownership drift (5), confused-deputy policy execution
(6), and replay asserting authority (7).

## Evidence originally presented

Live exploits against the running service on the **dispatcher** and the **policy
executor**: a forged delivery row whose self-consistent label matched the
webhook was delivered; a synthetic execution row paired a policy with another
tenant's event and exfiltrated it. Both were closed by
`domain.ResolveAndAuthorize`, and the relay/consumer fan-out was confined by
`domain.FanOut`.

## Fresh investigation

The class is *"a privileged consumer acting on tenant-owned work must not trust
what it was handed."* The consumer set was re-derived from the code rather than
from the ADR:

| Privileged consumer | Path | Re-derives? |
|---|---|---|
| Dispatcher | delivery | ✅ `ResolveAndAuthorize` against `audit_event` |
| Policy executor | execution | ✅ `ResolveAndAuthorize` against `audit_event` |
| Relay / policy consumer | production | ✅ `domain.FanOut` confines fan-out |
| Replay worker — **window path** | `replayWindow` | ✅ reads the audit log, applies `domain.SameOwner` |
| Replay worker — **by-id path** | `Requeue(job.DeliveryIDs)` | ❌ **nothing** |

The by-id path was never swept. `DeliveryOps.Requeue` was
`UPDATE webhook_delivery SET status='pending', retry_count=0 … WHERE id = ANY($1)`
executed on the **privileged pool**, with the ids taken verbatim from the replay
request body. The job's own `WebhookID` was not consulted, so neither the
delivery's owner nor the work's owner constrained the write.

## Current evidence

Live, against real PostgreSQL, with two tenants:

```
victim delivery before:  status=dead_letter  retry_count=3
requeue naming that id from the attacker's webhook → 1 row affected
victim delivery after:   status=pending      retry_count=0
```

A terminated delivery belonging to another tenant was resurrected with a
refilled retry budget (ADR-CONCURRENCY-006), so the victim's subscriber receives
it again. The *send* itself remains ownership-checked by the dispatcher, so this
is a cross-tenant **write and denial-of-integrity primitive**, not a data
disclosure.

After the fix (`Requeue(ctx, webhookID, ids)` with
`WHERE id = ANY($1) AND webhook_id = $2`): **0 rows affected**, victim row
unchanged.

## Confidence: PARTIALLY VALIDATED

**Reason.** The original evidence remains valid for the consumers it examined —
the dispatcher and executor exploits do not reproduce, and the relay, policy
consumer and replay-window paths all re-derive correctly. The *class* claim did
not hold: a fifth privileged path existed and re-derived nothing. Entries 5 and
6 were **INSTANCE CLOSED**, not CLASS CLOSED.

Entries 4 and 7 are **VALIDATED**: fan-out confinement and the replay window's
`SameOwner` check were confirmed present and correct by fresh inspection, and
ADR-TENANCY-005's claim that replay owns no *authority* still holds — the by-id
defect was a failure to confine a write, not an assertion of authority.

## Required Trust Register changes

- Entries 5 and 6 reopened and re-closed under **ADR-TENANCY-009**.
- New entry **28** recording the class with its scope and residual.
- Enforcement replaced: the guard now derives its subject set from
  `TenantOwnedTables` and fails any privileged mutation keyed only on a
  caller-supplied id set without an owning predicate.

## Residual risk

- The sweep recognises confinement by the presence of an owning predicate; a
  statement could name one and still scope it wrongly. The guard proves the
  predicate exists, not that it is correct.
- `Requeue` is confined to the webhook, which is the work the caller was given.
  It does not re-derive the *event* owner from the audit log — that check remains
  at send time in the dispatcher, which is where the outbound effect happens.
- The retention sweep (`DeleteOlderThan`) is deliberately owner-agnostic
  (ADR-TENANCY-008) and is excluded by the guard's id-set criterion.
