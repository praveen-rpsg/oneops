# ADR-GOV-004 — A delivery record holds the destination it was sent to, not a pointer to one

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-29 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-003/004 (ownership re-derived from the immutable log, never a mutable label), ADR-TENANCY-008 (operational tooling in scope), ADR-SECURITY-001 (outbound egress), ADR-CONCURRENCY-005/006 (the fenced outcome write this rides in) |

## Context

`webhook_delivery` recorded which subscription a delivery belonged to
(`webhook_id`) and nothing about where the request actually went. The
destination was obtainable only by joining to `webhook.url` — a mutable field on
a row any administrator may `PATCH`.

So the platform's record of where governed data was sent was **retroactively
rewritable**. Proven live against the running service:

```
delivery dlv_05d82a0f2a13c55127e797058a809d61
  destination as recorded:  http://127.0.0.1:9911/approved

  PATCH /v1/admin/webhooks/{id}  {"url":"http://127.0.0.1:9911/attacker-controlled"}  -> 200

  destination as recorded:  http://127.0.0.1:9911/attacker-controlled   ← same delivery row
```

One request, no audit event anywhere, and every delivery ever made through that
subscription now reads as having gone somewhere it never went. The signing
secret is unchanged by a URL patch, so the new destination also receives validly
signed governance events.

Two consequences follow, and the second is worse than the first:

- An investigator asking *"where was this event sent?"* is told the current URL,
  not the one used. The answer is confidently wrong.
- An actor who repoints, collects, and repoints back leaves the history reading
  as the **approved** destination for the entire window. The exfiltration is not
  merely unlogged; the record actively attests to the innocent destination.

This is the mirror image of a class the platform already closed. ADR-TENANCY-003
established that ownership must be re-derived from the immutable audit log
because a mutable label "is only a claim." Here an immutable fact — where a POST
went — was being derived *from* mutable state. The same principle, inverted.

The failure class is: **a record of a past event that stores a reference to
mutable configuration instead of the fact that occurred.**

### Decision gate — ADR, not CMR

The Constitution governs Configuration Objects and §8 operations. Webhook
subscriptions and delivery records are platform infrastructure; no ratified
clause speaks to them. The Constitution is **silent**, not contrary — this is not
an implementation faithfully following a ratified rule with undesirable results.
It is an engineering defect against the programme's own architectural law that no
state change may exist without an authoritative record. Engineering authority
applies; the outcome is an **ADR**.

## Decision

**A delivery record holds the destination of the attempt that was made, captured
at the moment it was made, and it is never derived from the subscription.**

1. **`webhook_delivery.delivered_to`** holds the URL the most recent attempt was
   sent to. `NULL` means no attempt has been made — a pending delivery has no
   destination fact, and back-filling one from the current webhook URL would
   manufacture the very claim this column exists to stop.

2. **It is written in the same fenced `UPDATE` that records the outcome.**
   `MarkResult` takes the destination and sets it alongside status, attempt count
   and status code, under the same `claimed_at` fence (ADR-CONCURRENCY-005). The
   destination is therefore exactly as trustworthy as the outcome, and an evicted
   worker can no more rewrite one than the other. A second write would be a
   second failure point.

3. **An outcome reached without an attempt records nothing and erases nothing.**
   An empty destination leaves the stored value untouched
   (`COALESCE($8, delivered_to)`). A delivery refused before it left the platform
   (subscriber gone, ownership refused) must not claim a destination; and a later
   dead-letter must not erase the record of where an earlier attempt really went.

4. **Reads never join to `webhook.url`.** The destination of a past delivery
   comes from the delivery row. Reading it through the subscription would restore
   the defect exactly.

5. **The fact is exposed to operators** as `delivered_to` on the delivery and
   dead-letter administration APIs, so the answer to "where did this go?" is
   available where an investigator looks.

## Consequences

**What is now guaranteed.** For every attempted delivery, the platform holds an
account of where that attempt was sent which cannot be altered by changing the
subscription. Repointing a webhook changes where *future* deliveries go and
leaves the record of past ones intact.

**What is *not* claimed.**

- **Only the most recent attempt's destination is retained.** If a subscription
  is repointed between retries of the same delivery, earlier attempts' URLs are
  not preserved — the row holds one destination, not a per-attempt history.
  Recording every attempt separately is a larger change (an attempts table) and
  is not claimed here.
- **This does not audit the administrative act.** Creating, patching or deleting
  a webhook, policy or tenant still writes no audit event — measured live: three
  admin creations produced a delta of **0** rows in `audit_event`. This ADR makes
  the *consequence* of a repoint visible in the delivery record; it does not
  record the repoint itself. That gap is real, is stated in the Trust Register as
  a residual, and is the recommended next investigation.
- **Rows written before this change have `NULL`.** Their destination is
  genuinely unknown, and `NULL` says so rather than guessing.
- **Delivery history is retention-bounded** (`ONEOPS_WEBHOOK_RETENTION_HOURS`,
  default 720h). The recorded fact expires with the row.
- **Behaviour of delivery is unchanged.** The dispatcher still resolves the URL
  live at attempt time, so documented rotation behaviour is preserved; the change
  is that it now writes down which URL it used.

## Evidence

Live exploit, before the change: a delivery POSTed to
`http://127.0.0.1:9911/approved` reported `http://127.0.0.1:9911/attacker-controlled`
as its destination after one `PATCH` returned 200, with zero audit events.

Live re-attack, after the change, against the rebuilt binary:

```
delivered_to (recorded at attempt): http://127.0.0.1:9911/approved
PATCH the subscription to           http://127.0.0.1:9911/attacker-controlled  -> 200
delivered_to (unchanged):           http://127.0.0.1:9911/approved
webhook.url (now):                  http://127.0.0.1:9911/attacker-controlled
```

The record and the current configuration now disagree, correctly, and the record
is the one that reflects what happened.

Full suite green under `-race` against real PostgreSQL, all 19 packages.

## Enforcement

- `arch.TestDelivery_RecordsItsOwnDestination` — the delivery carries a recorded
  destination and the port can accept it.
- `arch.TestDeliveryDestination_IsWrittenWithTheOutcome` — one `UPDATE`, and the
  `COALESCE` that stops an unattempted outcome erasing the fact.
- `arch.TestDispatcher_RecordsTheURLItPosted` — the dispatcher passes the URL it
  used, on the successful path specifically.
- `arch.TestDeliveryReads_DoNotDeriveDestinationFromTheWebhook` — no delivery
  read joins to a webhook URL.
- `postgres.TestDeliveryDestination_SurvivesWebhookRepointing` — the live exploit
  as a regression test.
- `postgres.TestDeliveryDestination_UnattemptedOutcomeRecordsNothing` — no
  invented destination, and no erasure of a real one.

Mutation-verified: stopping `MarkResult` writing `delivered_to`, and making a
successful delivery record an empty destination, each fail the build with the
architecture diagnostic naming this ADR, and the integration test fails with
`destination not recorded at attempt time`.
