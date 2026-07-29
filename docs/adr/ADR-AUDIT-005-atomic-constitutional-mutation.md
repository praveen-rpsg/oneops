# ADR-AUDIT-005: A governance mutation and its audit event commit in one transaction, or neither does

- **Status:** Accepted — **in force since v1.0.0 GA**. This file is a **back-fill record** (CMR work package WP-0.1; CA-3 item CMR-B07; CA-2 Annex A). The decision was previously documented only in code comments, the CHANGELOG, runbooks, and ADR-GOV-001; the original decision date is not separately recorded.
- **Date:** 2026-07-23 *(record created; decision predates it)*
- **Constitutional authority:** Vol IV §4.6 — the **Audit Contract** (*"an independently verifiable, tamper-evident record"*); Vol II §14 (Audit)
- **Laws engaged:** Vol II Part 11 — Law 12 (append-only history), Law 14 (reversibility)
- **Relates to:** `ADR-AUDIT-003`, `ADR-AUDIT-004` (the audit chain this atomicity protects); `ADR-GOV-001` (which rests on this ADR as its load-bearing premise); `ADR-CP5`, `ADR-CP6` (single-writer governance)
- **Provenance note:** every statement below is transcribed from an attested primary source and tagged. Nothing is inferred. Per CA-2's discipline, unrecorded matter is marked **[UNKNOWN]**, not filled in.

## Context

The governance engine performs the single authoritative mutation of a Configuration Object's dimensions (§8; ADR-CP5, ADR-CP6). Each such mutation must be accompanied by exactly one audit event (§8 *"Event row"*). The constitutional requirement is Vol IV §4.6's Audit Contract — an independently verifiable, tamper-evident record — which is defeated if a mutation can exist without its audit event, or an audit event without its mutation.

**[ATTESTED — `internal/governance/engine.go:69-72`]** *"the engine emits exactly one audit event from it within the same transaction, and mutation and audit event commit atomically (ADR-AUDIT-005)."*

## Decision

**The Configuration Object's dimensional change and its audit append commit in a single transaction, or neither does.**

**[ATTESTED — ADR-GOV-001, Context]** *"`ADR-AUDIT-005` establishes that the governance engine owns exactly one atomic mutation per operation: the Configuration Object's dimensional change and its audit append commit in a single transaction, or neither does."*

The two states this exists to forbid are named:

**[ATTESTED — ADR-GOV-001, Decision]** the guarantee forbids *"a committed audit event asserting an extension that has no edge, or an edge with no audit event."* Generalized to every operation: **no governance mutation without its audit event, and no audit event without its mutation.**

Realization, as attested in code:

- **[ATTESTED — `engine.go:145-152`]** the `Auditor` port *"appends exactly one sealed audit event for an operation, using the engine's OWN transaction so the mutation and its audit record commit atomically (ADR-AUDIT-005)."*
- **[ATTESTED — `engine.go:293-318`]** the ATOMIC AUDIT EMISSION block builds and appends exactly one event within the same transaction, followed by a **SINGLE ATOMIC COMMIT POINT**: *"One commit seals the governance mutation and its audit event together … the deferred Rollback undoes the mutation: a governance mutation can never exist without its audit event, nor an audit event without its mutation."*
- **[ATTESTED — `audit_appender.go:70-78`]** `AppendTx` is *"the transaction-scoped entry point ADR-AUDIT-005 introduces so the governance engine can append the audit event atomically inside its own transaction,"* with hashing/ordering/canonicalization/EventID *"byte-for-byte identical to the standalone path — only transaction ownership moves to the caller."*

## Consequences

**[ATTESTED across operational docs.]**

- **Easier / guaranteed.** Governance and audit cannot diverge. **[`docs/runbooks/audit-integrity.md:72`]** *"Governance and audit commit atomically (ADR-AUDIT-005), so a break indicates post-commit corruption/drift … not a partial write."*
- **Operational invariant — one database.** **[`docs/deployment.md:5`]** *"Governance and audit must share one database (operational invariant of ADR-AUDIT-005: the governance mutation and its audit append commit in one transaction)."* **[`docs/disaster-recovery.md:5`]** *"governance + audit are co-located and must be backed up together — ADR-AUDIT-005."*
- **Downstream dependence.** **[`docs/milestones/M4-validation-engine-plan.md:55`]** the event relay is *"audit-log-as-outbox … an event is delivered iff its operation committed (ADR-AUDIT-005) … no dual-write hazard."* **[`internal/events/events.go:6`; `internal/timeline/entries.go:14`]** both depend on the atomic commit.
- **Harder / bounded.** **[ADR-GOV-001, risk R3]** widening the transaction beyond *state + audit append* would break this ADR; it is an explicit architecture-review gate.
- **[UNKNOWN]** the alternatives considered at the original decision, and any options rejected, are not recorded in the surviving sources.

## Reversal

**[UNKNOWN]** — no reversal procedure is recorded in the attested sources. Recorded here as unknown rather than invented. Any change to the atomic transaction boundary is, per Engineering Implementation Guide Appendix B.3, itself an ADR-requiring decision and an architecture-review gate (ADR-GOV-001 R3).

---
*Back-filled under CMR work package WP-0.1 (CA-3 item CMR-B07) by the Configuration Authority, transcribing attested primary sources per CA-2 Annex A. This record documents an already-accepted decision; it makes no new decision, creates no obligation, and amends no constitutional text.*
