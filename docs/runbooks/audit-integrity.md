# Runbook: Audit Integrity Monitoring & Recovery

**Scope:** Operating the OneOps audit-integrity verification subsystem. The audit
chain is tamper-evident and append-only (Vol IV §4.6); a detected break is a
constitutional integrity event, not a routine error.

**Owner:** Platform / SRE. **Severity of a break:** P1.

---

## 1. What runs

The control plane starts an **audit-integrity scheduler** (`internal/ops`) when
`ONEOPS_AUDIT_VERIFY_INTERVAL_SECONDS > 0`. On each sweep it lists every audit
chain (`audit_chain_head`) and runs the **frozen** audit `Verifier` over it. It
changes no audit semantics — it only observes and reports.

| Setting | Env var | Default |
| --- | --- | --- |
| Sweep interval | `ONEOPS_AUDIT_VERIFY_INTERVAL_SECONDS` | 300 |
| Per-chain timeout | `ONEOPS_AUDIT_VERIFY_TIMEOUT_SECONDS` | 30 |
| Transport retries | `ONEOPS_AUDIT_VERIFY_RETRY_ATTEMPTS` | 2 |

Set the interval to `0` to disable (e.g. to run verification out-of-band).

## 2. Metrics (Prometheus, served on `/metrics`)

| Metric | Meaning |
| --- | --- |
| `oneops_audit_integrity_ok` | **1** healthy, **0** last sweep had a break or error |
| `oneops_audit_verification_failures_total` | Cumulative **integrity breaks** detected |
| `oneops_audit_verification_errors_total` | Cumulative transport/timeout errors |
| `oneops_audit_chains_verified_total` | Chains verified across all sweeps |
| `oneops_audit_verification_runs_total` | Completed sweeps |
| `oneops_audit_verification_duration_seconds` | Per-chain verification latency |
| `oneops_audit_last_verification_timestamp_seconds` | Freshness of the last sweep |

## 3. Alerts (suggested)

- **P1 — Integrity break:** `increase(oneops_audit_verification_failures_total[15m]) > 0`
  or `oneops_audit_integrity_ok == 0`.
- **P3 — Verification stalled:** `time() - oneops_audit_last_verification_timestamp_seconds > 3 * interval`.
- **P3 — Persistent errors:** `increase(oneops_audit_verification_errors_total[30m]) > 5`.

## 4. Respond to an integrity break (P1)

1. **Confirm.** Find the structured log line `audit integrity: CHAIN BREAK DETECTED`
   — it carries `chain_id`, `first_break_seq`, `reason`, `head_seq`, `checked`.
2. **Do not mutate the chain.** Audit history is append-only; never `UPDATE`,
   `DELETE`, or `TRUNCATE` `audit_event` (DB triggers already forbid this).
3. **Scope the blast radius.** The break is isolated to one `chain_id` (chains are
   per governed object). Other chains remain independently valid.
4. **Preserve evidence.** Snapshot the affected chain's rows for forensics:
   `SELECT seq, event_id, prev_hash, this_hash, occurred_at FROM audit_event
   WHERE chain_id = $1 ORDER BY seq;`
5. **Determine cause** — restore-from-backup drift, out-of-band write, or storage
   corruption. Correlate `occurred_at` around `first_break_seq` with deploys,
   restores, and DB access logs.
6. **Escalate** to the Constitutional Architecture Council; a break is a
   governance-level event, not a routine incident.

## 5. On-demand verification

The same engine backs ad-hoc checks: construct `ops.New(...)` and call
`RunOnce(ctx)`; the returned `IntegrityReport` (`Healthy()`, `Failures`, `Errors`)
is safe to log or surface to an operator. No separate tooling or DB writes.

## 6. Recovery principles

- A break is **detected and reported**, never auto-repaired — repair would itself
  be a mutation of the tamper-evident record.
- Governance and audit commit atomically (ADR-AUDIT-005), so a break indicates
  post-commit corruption/drift (backup restore, storage fault), not a partial
  write. Investigate durability of the PostgreSQL volume/backups first.
- After root-cause, remediation is a governance decision recorded via the
  amendment/ADR process — not an operational hotfix.
