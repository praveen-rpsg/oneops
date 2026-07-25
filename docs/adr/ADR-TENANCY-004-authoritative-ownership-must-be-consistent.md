# ADR-TENANCY-004 — Authoritative ownership must be consistent, and verified against its root

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-25 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-003 (execution ownership is re-derived), ADR-AUDIT-003/005 (append-only audit, atomic mutation) |

## Context

ADR-TENANCY-003 made every privileged outbound action re-derive event ownership
from `audit_event.tenant_id` rather than trust the queue row. That defeated
forged queue metadata. It rested on one assumption: that `audit_event` is
authoritative.

Trust-infrastructure validation attacked that assumption and broke it.

`audit_event.tenant_id` is not the root of ownership. It is a denormalized copy,
written beside the real owner. The root is the governed object the chain is
about: a chain's id is the object's `cfg_id`, and `configuration_object.tenant_id`
is RLS-enforced at write time and cannot be set cross-tenant. The audit column is
a copy of that value, added by migration with a `system` backfill for legacy
rows, and protected only against modification — not against being written wrong,
nor against diverging from the object it records.

The divergence is reachable without touching the application. `audit_event`
forbids UPDATE, DELETE and TRUNCATE, but not the INSERT of a new sequence into an
existing chain. A partial restore, an operator repair script, a legacy backfill,
or split-brain history can leave a chain whose events name a different tenant
than its object.

Verified against the running service: an event appended to a victim's chain but
labelled with an attacker's tenant was fanned out to the attacker's webhook and
delivered — the victim's chain id, operation and actor reached the attacker's
endpoint. The execution-time resolver "confirmed" the attacker as owner, because
the resolver and the corrupted value were the same row. Re-deriving from a single
denormalized copy is not re-derivation; it is trusting the copy.

## Decision

**Authoritative ownership is the governed object's owner, and the audit log must
agree with it. Disagreement is corruption, and the platform fails closed on it —
at execution, and at startup.**

1. **Resolution reads two independent records and requires agreement.**
   `ResolveEventOwner` joins the audit event to its governed object on
   `cfg_id = chain_id`, returns the object's owner, and refuses with
   `ErrOwnershipAmbiguous` if the audit row's own label disagrees. An attacker
   must now corrupt both records identically; corrupting either alone is
   detected. A chain with no object (deleted or never created) is
   `ErrEventNotFound` — unresolvable, and refused.

2. **Execution fails closed.** The dispatcher and the policy executor already
   treat any resolver error as a refusal and dead-letter; `ErrOwnershipAmbiguous`
   flows through unchanged. Ambiguous authority never produces an outbound
   action.

3. **Startup fails closed.** `ValidateOwnershipConsistency` scans for events
   whose tenant diverges from their object's, and the control plane refuses to
   boot when any exist, naming an example chain. The platform will not begin
   performing outbound actions on a log whose authority it cannot trust. It does
   not silently repair the divergence — repair is an operator decision, because
   the platform cannot know which of the two records is the corrupted one.

## Consequences

**Orphaned chains are legitimate and are not flagged.** Append-only audit
outlives the object it records: a deleted object leaves its chain behind. The
startup scan checks only divergence (object present and disagreeing), not
absence; the runtime resolver refuses an orphan as unresolvable, which is safe
because a deleted object has no new events to deliver.

**Legacy rows remain consistent.** Both `configuration_object.tenant_id` and
`audit_event.tenant_id` were backfilled to `system`, so pre-tenancy chains agree
with themselves and resolve to `system`. No false alarm, and no legacy
disclosure.

**The residual is narrowed, not eliminated.** An attacker who can write both the
object and the audit rows consistently still owns the data by definition —
they hold the database. What is now closed is single-source corruption:
restores, operator scripts and split-brain that touch one record and not the
other. The platform detects those rather than trusting them, which is the
realistic infrastructure-imperfection threat this phase targets.

**No database constraint enforces this.** `audit_event` is partitioned, so a
foreign key to `configuration_object` is not available, and a cross-row CHECK
cannot express it. The invariant is therefore enforced by the resolver at
execution, by the scan at startup, and by tests — not by the schema. That is a
weaker guarantee than a constraint and is recorded as such; a validating trigger
was considered and rejected because it would couple the append-only audit schema
to the configuration schema and complicate the genesis transaction.

## The invariant

A denormalized ownership label is not authoritative merely because it is
append-only. Authority is the root record — the governed object — and every copy
of it must be verified against that root before it is trusted. Where the copy and
the root disagree, ownership is ambiguous, and an ambiguous authority is refused,
never guessed: at execution, and before the platform will start.
