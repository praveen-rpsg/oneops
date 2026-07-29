# ADR-TENANCY-011 — A cursor never outruns its log

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-29 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-006 (recovery is a verification boundary), ADR-CONCURRENCY-004 (monotonic cursor / no lost events — where this gap was recorded and left open), ADR-SECURITY-003 (the invariant registry this joins), **EVR-008** |

## Context

From the Trust Register audit recorded in EVR-008. Entry 8 records
*"restore-inconsistency (an inconsistent backup silently trusted)"* as an
eliminated class. ADR-CONCURRENCY-004's own boundary dossier records an instance
that was never closed:

> A cursor restored ahead of its log would skip the missing range; a periodic
> "no cursor exceeds its chain head" check is noted future hardening.

Nothing checked it. The class therefore had an open instance recorded in the
programme's own documentation for as long as the entry claimed to be closed.

The failure is silent by construction. `webhook_cursor` and `policy_cursor` are
watermarks: the relay and the policy consumer deliver everything *after*
`last_seq`. A watermark that is too high is indistinguishable from one that is up
to date, so every event in between is simply never read — no error, no retry, no
dead letter.

The platform cannot reach this state on its own: the cursor is monotonic and only
advances past events already enqueued. It is reachable through **recovery** — a
restore taking the cursor tables from a newer snapshot than `audit_event`, or a
partial restore of the log. That is precisely the case ADR-TENANCY-006 exists
for.

## Decision

**A cursor ahead of its chain head is a platform invariant violation.**

`CursorValidator` reports any `webhook_cursor` or `policy_cursor` row whose
`last_seq` exceeds its chain head — or that has no chain head at all, which is
the same claim in a stronger form: progress through a log that does not exist.

It is **registered in `platformInvariants`**, so it gains the boot gate and the
continuous sentinel at once (ADR-SECURITY-003). That is the registry paying for
itself: closing this gap required writing the check and adding one entry, not
wiring two enforcement points.

## Consequences

**What is now guaranteed.** A restore that leaves a cursor ahead of the log is
refused at boot and detected continuously, instead of silently skipping events.

**What is *not* claimed.**

- This detects the inconsistency; it does not repair it. Recovery remains a
  verification boundary, not a repair mechanism (ADR-TENANCY-006).
- A cursor *behind* its log is normal and is not reported — that is simply work
  still to do.
- The check compares against `audit_chain_head`. A restore that rewound both the
  log and the head consistently is, by construction, consistent.

## Evidence

With a chain head at seq 5 and a cursor at seq 99:

```
webhook_cursor has 1 cursor(s) ahead of the audit log
(e.g. chain cursor-restore-… at seq 99, log head 5)
— the event relay would silently skip every event in between
```

Before this ADR, nothing reported it.

## Enforcement

- `postgres.TestCursorValidator_DetectsACursorAheadOfItsLog` — the gap as a
  regression test, with a clean-database control first so it cannot pass
  vacuously.
- `arch.TestEveryValidator_IsRegisteredAsAnInvariant` — now **tree-derived**.
  Its earlier version named two validator files and so did not notice this third
  validator when it was added: the enumerated-enforcement pattern appearing in
  this programme's own enforcement. Discovering validators from the tree is what
  makes "every validator is registered" a checkable claim.
