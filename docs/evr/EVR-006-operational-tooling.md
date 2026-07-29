# EVR-006 — Operational tooling in the trust model (entry 10)

| | |
|---|---|
| **Date** | 2026-07-29 |
| **Auditor** | Acting CTO / Evidence Authority |
| **Trust Register entry** | 10 |
| **Associated ADR** | ADR-TENANCY-008 |
| **Confidence** | **PARTIALLY VALIDATED** — one half validated, one half **INVALIDATED**: a sibling instance existed and was defective. ECL-2 → ECL-5. |

## Ranking

Entry 10 scored **7.50**, second in the weighted ranking: enforcement tier
integration-only (Level 1), architectural authority high (it protects audit
immutability, on which audit-derived ownership rests).

## Original claim

ADR-TENANCY-008 decided two things: (1) startup validates the audit-immutability
triggers; (2) *"no new privileged binary may exist unregistered"*, with an
architecture test forcing a reviewer to state how it obtains ownership — because
a security decision must never be *"encoded in a script"*.

## Fresh investigation

**Half 1 — VALIDATED and strengthened elsewhere.** Audit-immutability validation
is no longer merely a startup check: ADR-SECURITY-003 put it under the continuous
invariant sentinel. Maturity for that half is already Level 4.

**Half 2 — the guard exists, but its subject set is `cmd/` only.** I initially
read the guard as absent and was wrong; a second, more careful search found
`TestOperationalBinariesAreRegistered` walking `cmd/`. Recorded because a
false finding reported as fact would have been worse than the defect: the first
grep was too narrow, and I confirmed before concluding.

**Sibling instance found and defective.** ADR-TENANCY-008's own context table
inventories three *scripts*, and its reasoning names scripts explicitly — but the
enforcement never covered them. `dr-drill.sh` derives its target database from
`DR_DRILL_DB` and drops it, with **zero** expressions comparing that name to the
live database.

## Fresh evidence

Derived from the script's own variables against the live DSN, without executing
the drop:

```
DR_DRILL_DB=oneops_drdrill -> DROP DATABASE IF EXISTS oneops_drdrill  (live: oneops)  safe
DR_DRILL_DB=oneops         -> DROP DATABASE IF EXISTS oneops          (live: oneops)  <<< DROPS PRODUCTION
guard expressions found: 0
```

After the fix: exit code **1**, a named refusal, the live database untouched, and
a legitimate throwaway target still proceeds.

## Root cause

Enforcement whose subject set was narrower than the decision it enforced. The ADR
reasoned about scripts and guarded only binaries — a variant of the enumerated-
enforcement pattern, and the sixth audit in seven to find it.

## Authority produced

**EVR-006** and **ADR-TENANCY-010**.

## Class status — CLASS CLOSED

Both privileged surfaces — `cmd/` and `scripts/` — are now registry-guarded, and
the specific destructive failure is pinned by its own guard.

## Evidence confidence — ECL-5 (from ECL-2) · Enforcement maturity L1 → L4

Directory-derived discovery over both surfaces, registry-forced justification,
mutation verified in three directions plus vacuity and a live positive control.

## Residual risk

- The registries record *that* a justification exists, not that it is true.
- The refusal compares database names, so a drill pointed at a different host
  holding a production database under another name is not detected. The guard
  addresses the mistake reachable from the documented workflow.
- `db-restore.sh` restores over whatever target it is given by design; its safety
  rests on ADR-SECURITY-003's continuous invariants.

## Process note

While mutation-testing, a `git checkout` intended to revert a mutation also
reverted the new, uncommitted guards. It was caught by re-checking the file
rather than trusting the passing test that followed — a passing suite after a
revert is exactly the false confidence this programme treats as a defect.
