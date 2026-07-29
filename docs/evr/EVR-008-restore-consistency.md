# EVR-008 — Restore consistency (entry 8)

| | |
|---|---|
| **Date** | 2026-07-29 |
| **Trust Register entry** | 8 |
| **Associated ADR** | ADR-TENANCY-006 |
| **Confidence** | **PARTIALLY VALIDATED** — a known, documented instance was open. ECL-2 → **ECL-5**; maturity **L1 → L4**. |

## Ranking

Scored **6.90**, fourth: integration-only enforcement, persistence authority.

## Original claim

Recovery is a verification boundary: a restored database may be internally
inconsistent, and the platform must prove ownership can be established before it
accepts traffic. Enforced by the ownership validator at startup.

## Fresh investigation

**Half validated.** The ownership validator now runs at boot *and* continuously,
having been placed in the invariant registry by ADR-SECURITY-003. That half of
entry 8 is at maturity Level 4.

**A sibling was open, and the programme had already written it down.**
ADR-CONCURRENCY-004's dossier records: *"A cursor restored ahead of its log would
skip the missing range; a periodic 'no cursor exceeds its chain head' check is
noted future hardening."* Nothing checked it. The class "an inconsistent restore
silently trusted" therefore had a **known** open instance for as long as the
entry claimed closure — the most avoidable kind of overstatement, because no
discovery was required to find it.

## Fresh evidence

```
chain head at seq 5, webhook_cursor at seq 99
before: no check exists — the relay silently skips 94 events
after:  "webhook_cursor has 1 cursor(s) ahead of the audit log … would silently
         skip every event in between"
```

## Authority produced

**EVR-008** and **ADR-TENANCY-011**.

## Class status — CLASS CLOSED · ECL-5 · maturity L1 → L4

The new `CursorValidator` is registered in `platformInvariants`, so it gained the
boot gate and the sentinel from one line — the invariant registry paying for
itself.

## Verification self-audit

Adding the validator exposed a defect **in this programme's own enforcement**:
`TestEveryValidator_IsRegisteredAsAnInvariant` named two validator files, so it
did not notice a third validator when one was added. It now discovers validators
from the tree. Enumerated enforcement, found once again — this time in the guard
written to prevent exactly this.

The regression test runs a clean-database control before seeding the
inconsistency, so it cannot pass vacuously.

## Residual risk

- Detection, not repair — recovery remains a verification boundary.
- A cursor *behind* its log is normal and unreported.
- A restore that rewound log and head consistently is consistent by construction
  and is not a target of this check.
