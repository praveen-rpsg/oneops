# ADR-AUDIT-003: The audit record is tamper-evident, append-only, and caller-transaction-composable

- **Status:** Accepted — **in force since v1.0.0 GA**. This file is a **partial back-fill record** (CMR work package WP-0.1; CA-3 item CMR-B09; CA-2 Annex A). The decision's rationale was documented only in code comments and the CHANGELOG; the original decision text and date are not separately recorded.
- **Date:** 2026-07-23 *(record created; decision predates it)*
- **Constitutional authority:** Vol IV §4.6 — the Audit Contract (*"independently verifiable, tamper-evident record"*); Vol II §14 (Audit)
- **Laws engaged:** Vol II Part 11 — Law 12 (append-only history)
- **Relates to:** `ADR-AUDIT-004` (the 003/004 boundary is **UNRECOVERABLE** — see below); `ADR-AUDIT-005` (atomic mutation over this record)
- **Recovery status:** ⚠️ **PARTIALLY RECOVERED.** Only jointly-attested properties survive; **no content is solo-attributed to ADR-003 anywhere in the corpus**, so its distinct decision text is **[UNKNOWN]**.

## Context

The OneOps audit is a tamper-evident, hash-chained, append-only record whose integrity is a governance obligation (Vol IV §4.6; Vol II §14).

**[ATTESTED — `CHANGELOG.md:46`]** *"Audit Integrity — tamper-evident, hash-chained, append-only audit (ADR-AUDIT-003/004)."*

## Decision (jointly-attested content only)

**The audit record is tamper-evident, hash-chained, and append-only; its atomic-composition methods accept a caller-owned transaction.**

**[ATTESTED — `internal/store/postgres/audit_store.go:14-19`]** the store *"persists the tamper-evident audit chain (audit_event) and its per-chain head (audit_chain_head). It is persistence only … Methods that must be composed atomically accept a pgx.Tx so **the caller owns the transaction (ADR-AUDIT-003/004)**."*

## Boundary with ADR-AUDIT-004

⚠️ **UNRECOVERABLE (CA-2 §7 R-4).** **ADR-AUDIT-003 has no solo citation anywhere in the corpus** — every reference to it (`audit_store.go:19`, `CHANGELOG.md:46`, `internal/ops/integrity.go:5`) is joint with ADR-004 (and sometimes 005). The properties recorded above (tamper-evident · hash-chained · append-only · caller-owned-transaction) are therefore attested to the **pair**; **which of them ADR-003 uniquely decided cannot be determined from the surviving corpus.** They are recorded in both this file and ADR-AUDIT-004, flagged boundary-uncertain, so that no property is silently lost and none is falsely attributed.

## Consequences

- **[ATTESTED — `internal/ops/integrity.go:5`]** the audit Verifier treats *"ADR-AUDIT-003/004/005 as untouched"* — this record is stable and read-only to later work.
- **[UNKNOWN]** the distinct decision ADR-003 made relative to ADR-004; alternatives considered.

## Reversal

**[UNKNOWN]** — not recorded in the attested sources.

---
*Back-filled under CMR work package WP-0.1 (CA-3 item CMR-B09) by the Configuration Authority, transcribing attested primary sources per CA-2 Annex A. Partial by necessity: no content is solo-attributable to this ADR, and the 003/004 boundary is unrecoverable. This record documents an already-accepted decision; it makes no new decision and amends no constitutional text.*
