# AR-001 — How the platform records a historical fact

| | |
|---|---|
| **Status** | **DECIDED — 2026-07-29.** See *Decision*. Implementation of instances 2 and 3 may proceed under it. |
| **Date** | 2026-07-29 |
| **Author** | Acting CTO / Architecture Authority |
| **Related** | ADR-GOV-004 (delivery destination — one instance of this class, closed), ADR-TENANCY-003/004 (ownership re-derived from the immutable log), ADR-CONCURRENCY-005 (the fenced outcome write a snapshot would ride in), ADR-SECURITY-001 (policy action URLs are an egress surface) |
| **Blocks** | Any fix to `policy_execution` action provenance and to delivery header rendering |

## Why this is an Architectural Review and not an ADR

The defect is not in doubt. What is in doubt is the *rule* the platform should
adopt for recording history — and that rule binds every historical record the
platform will ever keep, not just the two open instances. Four designs are
defensible, they differ materially in storage cost, secret exposure,
reconstructability and operational ergonomics, and the Constitution is silent on
all of it (these are platform records, not Configuration Objects).

Choosing one inside a bug fix would set a platform-wide precedent invisibly,
which the programme's law forbids: *engineering shall not hide architectural
choices inside implementation*. Hence AR, and hence no implementation until the
decision is recorded.

## The class, and a correction to the record

**The class: a historical record whose content is resolved from current mutable
state at read time.**

ADR-GOV-004 closed one instance — a delivery's destination, which was obtained by
joining to the mutable `webhook.url`. **That ADR's Trust Register entry (#25)
overstated the result.** The register's own admission rule is *"eliminate the
class, not the instance — a fix that closes one path but leaves the category open
is not an entry here."* Two siblings were left open, and this AR exists partly to
correct that claim.

### Instance 2 — a policy execution does not record what it did (OPEN)

`policy_execution` snapshots the *event* (`event jsonb`) but not the *action*. The
action is `policy.action_type` + `policy.action_config`, both wholesale-replaceable
through `PATCH /v1/admin/policies/{id}`.

Proven live against the running service:

```
execution exec_9d59eb37f387ade301481123fee372df
  action as recorded:  webhook  {"url":"http://127.0.0.1:9911/approved-action"}

  PATCH /v1/admin/policies/{id}  {"action":{"type":"http","config":{"url":".../attacker-action"}}}  -> 200

  action as recorded:  http     {"url":"http://127.0.0.1:9911/attacker-action"}   ← same execution row
audit events for the change: 0
```

This is materially worse than the delivery case it mirrors. A policy action is
*active*: it POSTs governance event data outbound (the egress surface
ADR-SECURITY-001 guards). So the record of what the platform *did on its own
initiative* to a tenant's governance events is retroactively rewritable, and the
repoint → collect → repoint-back pattern leaves the history attesting to the
approved action throughout.

### Instance 3 — a delivery's headers are re-minted at read time (OPEN)

`GET /v1/admin/webhooks/{id}/deliveries/{deliveryID}` returns `headers` built by
`DeliveryView(d, wh.Secret, time.Now().UTC())`. The `X-OneOps-Timestamp` is
*now*, and the signature is computed over that fresh timestamp with the
subscription's *current* secret. The headers shown for a past delivery were
therefore never the headers sent.

Consequence: an operator diffing this against a receiver's logs finds a mismatch
that means nothing, and cannot use the platform to establish what the receiver
should have verified. After a secret rotation the displayed signature is
unrelated to anything that ever crossed the wire.

## Options

### Option A — Snapshot the fact into the record

Store the action (`action_type`, `action_config`) on `policy_execution`, and the
sent headers on `webhook_delivery`, written at execution/attempt time in the same
fenced write as the outcome (ADR-CONCURRENCY-005).

- **Reconstructability**: complete. The record answers the question by itself.
- **Storage**: duplicates config on every execution row. At policy-execution
  volumes this is small, but it grows with event volume, not with policy count.
- **Secret exposure**: the significant risk. `action_config` is free-form
  `jsonb` with pluggable backends (`EmailSender` and any custom action), so a
  config may hold credentials. Snapshotting copies them into a long-lived,
  retention-bounded table, widening the blast radius of a read of that table and
  defeating secret rotation for historical rows. Today's built-in HTTP action
  carries only `{"url": ...}` — the risk is forward-looking, not present.
- **Precedent**: "records are self-contained." Simple to reason about.

### Option B — Version the configuration and reference the version

Introduce immutable policy (and webhook) revisions; the execution records
`policy_version`. A `PATCH` creates a new revision rather than mutating.

- **Reconstructability**: complete, and the full change history comes free —
  which also answers the *separate* open gap that administrative changes are
  unaudited.
- **Storage**: one row per change rather than per execution. Cheapest at volume.
- **Secret exposure**: unchanged — secrets stay in one place with one lifecycle.
- **Cost**: the largest change. Touches the policy and webhook mutation paths,
  needs migration of existing rows, and introduces revision lifecycle questions
  (retention, deletion of a revision still referenced by history).
- **Precedent**: "configuration is immutable and versioned; history points at
  versions." This is the strongest long-term model and the one most consistent
  with how the platform already treats the audit log.

### Option C — Record a digest of the fact

Store a content hash of the action/headers on the record.

- **Reconstructability**: none. It proves the current config *is not* what ran,
  but cannot say what did.
- **Storage**: negligible. **Secret exposure**: none.
- **Assessment**: detects tampering without enabling investigation. For an
  egress record — where the operative question is *"where did our data go?"* —
  knowing only that the answer changed is close to useless.

### Option D — Make the fact immutable on the configuration

Forbid changing a policy's action (and a webhook's URL) in place; require
creating a new policy/subscription.

- **Reconstructability**: complete, with no new storage.
- **Secret exposure**: none.
- **Cost**: operationally awkward in exactly the cases that matter — rotating a
  URL or a credential inside an action config would force recreating the policy,
  orphaning its execution history from the "same" logical policy.
- **Assessment**: correct in principle (identity includes behaviour), poor
  ergonomics; likely to be worked around by operators, which is worse than the
  defect.

## Recommendation

**Option B, with Option A as the bounded interim if B cannot be scheduled.**

Reasoning:

1. B is the only option that makes configuration history a first-class fact
   rather than a copy. It fixes both open instances *and* subsumes the separate
   open gap — that creating, patching or deleting a webhook, policy or tenant
   currently writes no record at all (measured live: three admin creations
   produced a delta of **0** rows in `audit_event`).
2. B keeps secrets in one place with one lifecycle. A is the option most likely
   to age badly, precisely because `action_config` is free-form and pluggable —
   the moment one action type carries a token, every historical execution row
   holds a copy of it.
3. C is rejected: an egress record that cannot say where data went does not
   answer the question the record exists to answer.
4. D is rejected: it is principled but pushes operators toward workarounds.

If B is deferred, A is acceptable **only** with an explicit config-redaction rule
(snapshot with declared-secret fields removed), so the interim does not create
the secret-duplication problem B was chosen to avoid.

## Decision

**Adopted rule: *snapshot the facts of an act; version the configuration that
authorises it.***

Testing the options against both open instances exposed a flaw in the
recommendation above, and it is recorded rather than quietly fixed: **Option B
alone does not close instance 3.** A delivery's headers carry a signature over a
timestamp, and `DeliveryView` recomputes that timestamp as *now*. Versioning the
secret makes the *key* reconstructable but leaves the *timestamp* invented, so
the displayed signature still never crossed the wire. B answers "which secret",
not "what was sent". One option was never going to fit both instances, because
the two are different kinds of missing fact:

- **A fact produced at the moment of the act** — the timestamp that was signed,
  the URL that was dialled. Non-secret, small, meaningless anywhere but on that
  record. **Snapshot it (Option A).**
- **Configuration that authorised the act** — a policy's action, a
  subscription's secret. Potentially secret-bearing, shared across many acts,
  with its own lifecycle. **Version it and reference the version (Option B).**

Concretely:

1. **`policy_execution` references an immutable policy revision** (Option B).
   Every `PATCH` of a policy creates a revision; executions record which one they
   ran under. Secrets stay in one place with one lifecycle, and the change
   history of the policy becomes a first-class record — which also closes the
   separate gap that administrative changes are unrecorded.
2. **`webhook_delivery` records the signing timestamp it actually used**
   (Option A), alongside `delivered_to`, in the same fenced write as the outcome.
   `DeliveryView` renders historical headers from the recorded timestamp and the
   revision's secret, never from `now` and never from the current secret.
3. **Option A is not used for `action_config`.** Snapshotting a free-form,
   pluggable config into every execution row is the one path that duplicates
   secret material into long-lived storage, and it is rejected for that reason
   even though it is the cheapest to build.
4. **Option C (digest) and Option D (immutability) are rejected** on the grounds
   given above: C cannot answer the question the record exists to answer; D
   pushes operators toward workarounds.

**Sequencing.** Instance 3 (Option A, small, non-secret) may be implemented
immediately. Instance 2 (Option B) is a larger change touching the policy
mutation path and requires migration of existing policies to a revision 1; it
should be scheduled as its own investigation and must not be smuggled into an
unrelated change. Until it lands, `policy_execution` action provenance remains a
**known open defect**, recorded in the Trust Register scope note rather than
implied to be closed.

**If B is deferred beyond one cycle**, the interim is Option A *with a declared
redaction rule* (snapshot the action config with declared-secret fields removed)
— never an unredacted snapshot.

## Decision criteria

The Architecture Authority should choose on these, in order:

1. **Can an investigator reconstruct what the platform did, months later, from
   the record alone?** (A: yes. B: yes. C: no. D: yes.)
2. **Does the design duplicate secret material into long-lived records?**
   (A: yes, unless redacted. B/C/D: no.)
3. **Does it also close the unaudited-administrative-change gap, or leave it for
   a second mechanism?** (B: closes it. A/C/D: leave it.)
4. **Does it introduce a second history implementation?** A second audit-like
   store is itself the duplicated-authority smell this phase exists to
   eliminate; B extends one model, A/C add fields to existing records.
5. **Cost and migration risk**, weighed last — correctness of the record is the
   point.

## What is deliberately not decided here

- Whether administrative changes require a full audit trail, and where such a
  trail would live. `audit_event` is keyed by `chain_id = cfg_id` and the relay
  fans out every chain to subscribers, so administrative events placed there
  would leak administrative activity to tenants. That is a real constraint on
  any design and is noted, not resolved.
- Retention of historical records (all of these are bounded by
  `ONEOPS_WEBHOOK_RETENTION_HOURS`, default 720h). A recorded fact that expires
  is a separate question from a recorded fact that is wrong.

## Status

**OPEN.** No implementation of instances 2 or 3 may proceed until this review is
decided and recorded. Instance 1 (ADR-GOV-004) is closed and unaffected; its
Trust Register entry has been corrected to claim the instance rather than the
class.
