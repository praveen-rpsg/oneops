# ADR-AUDIT-004: The audit-chain append sequence and chain-head handling

- **Status:** Accepted — **in force since v1.0.0 GA**. This file is a **partial back-fill record** (CMR work package WP-0.1; CA-3 item CMR-B08; CA-2 Annex A). The decision's rationale was documented only in code comments and the CHANGELOG; the original decision text and date are not separately recorded.
- **Date:** 2026-07-23 *(record created; decision predates it)*
- **Constitutional authority:** Vol IV §4.6 — the Audit Contract (*"independently verifiable, tamper-evident record"*); Vol II §14 (Audit)
- **Laws engaged:** Vol II Part 11 — Law 12 (append-only history)
- **Relates to:** `ADR-AUDIT-003` (the 003/004 boundary is **UNRECOVERABLE** — see below); `ADR-AUDIT-005` (which appends atomically through this sequence)
- **Recovery status:** ⚠️ **PARTIALLY RECOVERED.** The step sequence and chain-head handling are solo-attested; the decision text, alternatives and reversal are **[UNKNOWN]**.

## Context

The OneOps audit is a tamper-evident, hash-chained, append-only record.

**[ATTESTED — `CHANGELOG.md:46`]** *"Audit Integrity — tamper-evident, hash-chained, append-only audit (ADR-AUDIT-003/004)."*

This ADR records the append sequence and the per-chain head handling.

## Decision (recovered content)

**The audit append follows a fixed step sequence within one transaction, and the chain head is created idempotently before a locking read.**

**[ATTESTED — `internal/store/postgres/audit_appender.go:70-72`]** the append flow is: *"EnsureChainHead → ReadChainHead(FOR UPDATE) → ChainHash → AppendAuditEvent → UpsertChainHead (**ADR-AUDIT-004 steps, unchanged**)."*

**[ATTESTED — `internal/store/postgres/audit_store.go:55-58`]** `EnsureChainHead` *"creates the genesis head row for chainID if it does not exist (last_seq 0, the caller-supplied genesis hash), and is a no-op otherwise. It runs in the caller's transaction so a locking ReadChainHead can follow without a create race (**ADR-AUDIT-004**). genesisHash must be 32 bytes."*

## Boundary with ADR-AUDIT-003

⚠️ **UNRECOVERABLE (CA-2 §7 R-4).** Every citation that names both ADRs is joint — `audit_store.go:19` (*"the caller owns the transaction (ADR-AUDIT-003/004)"*), `CHANGELOG.md:46` (*"(ADR-AUDIT-003/004)"*), `internal/ops/integrity.go:5` (*"(ADR-AUDIT-003/004/005 are untouched)"*). **Which decision belongs to ADR-003 versus ADR-004 cannot be determined from the surviving corpus.** The step-sequence and genesis-hash/create-race items above carry a **solo** ADR-004 citation and are therefore attributed here; the tamper-evident/append-only and caller-owned-transaction properties are joint and are recorded in both this file and ADR-AUDIT-003 as boundary-uncertain.

## Consequences

- **[ATTESTED — `ADR-AUDIT-005`, and `internal/ops/integrity.go:5`]** the audit subsystem's Verifier and this chain are *"untouched"* by later work; ADR-AUDIT-005's atomic path reuses this sequence *"byte-for-byte identical."*
- **[UNKNOWN]** alternatives considered; why this sequence over another.

## Reversal

**[UNKNOWN]** — not recorded in the attested sources.

---
*Back-filled under CMR work package WP-0.1 (CA-3 item CMR-B08) by the Configuration Authority, transcribing attested primary sources per CA-2 Annex A. Partial by necessity: only solo-attested content is attributed; the 003/004 boundary is unrecoverable and marked as such. This record documents an already-accepted decision; it makes no new decision and amends no constitutional text.*
