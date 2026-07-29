# EVR-004 — Audit-chain append authority (entry 11)

| | |
|---|---|
| **Date** | 2026-07-29 |
| **Auditor** | Acting CTO / Evidence Authority |
| **Trust Register entry** | 11 |
| **Associated ADR** | ADR-AUDIT-004 (audit chain append steps) — itself a CA-2 reconstruction with sections marked UNRECOVERABLE |
| **Confidence** | **PARTIALLY VALIDATED** — class confirmed closed, **evidence and enforcement were insufficient**. ECL-2 → ECL-5. |

## Ranking justification

Selected by the Primary Triage Law, not by subject. Measured enforcement tiers of
the eleven unswept entries:

| Tier | Entries |
|---|---|
| **Integration-only (no architecture tier)** | **1, 8, 10, 11, 13, 16** |
| startup + integration | 9 |
| arch (+ int / unit) | 3, 12, 23, 24 |

Integration-only is the weakest tier present. Applying the Secondary Triage Law
to those six by architectural authority, **entry 11 ranks first**: the chain-head
lock is a *platform-wide invariant* on which ADR-CONCURRENCY-004's gapless prefix
("no lost events") and ADR-TENANCY-003/004's audit-derived ownership both depend.
Entries 1 and 10 are authority boundaries (lower), 13 and 16 distributed
correctness, 8 persistence.

## Original claim and evidence

The append follows a fixed sequence inside one transaction:
`EnsureChainHead → ReadChainHead(FOR UPDATE) → ChainHash → AppendAuditEvent →
UpsertChainHead`. The head is created idempotently *before* a locking read, so
there is no create race.

The ADR is a reconstruction: its boundary with ADR-AUDIT-003 is explicitly marked
**UNRECOVERABLE**, which is itself an evidence-quality signal.

## Fresh investigation

**Sibling search: none found.** Exactly one production append path exists
(`audit_appender.go`), and it follows the sequence correctly. `AppendAuditEvent`
has no other caller.

**The defect is in the enforcement, not the implementation.** The lock was
expressed as a *boolean argument* — `ReadChainHead(ctx, tx, chain, forUpdate)`.
The platform's most load-bearing invariant was one `true`, guarded by a comment
and an integration test, with no architecture tier at all. The unsafe form was
reachable by forgetting an argument.

## Fresh evidence

Original evidence reproduced, and the invariant proven load-bearing:

```
with the chain-head lock:    12 concurrent appends -> seqs [1 2 3 4 5 6 7 8 9 10 11 12]
without it: two transactions both observe last_seq=0 and both claim seq 1
            tx1 commits seq 1; tx2's append is rejected; chain holds [1]
```

The second governance event is **lost**. Note the honest bound: the
`(chain_id, seq)` unique constraint means the failure is *fail-closed* — the
losing transaction errors rather than silently corrupting the chain — so the
consequence is lost operations, not a forged history.

## Root cause

Enumerated/absent enforcement for the fifth consecutive audit, in its weakest
form: no architecture tier at all, and an invariant encoded as a boolean
parameter.

## Authority produced

**ADR-AUDIT-006** — the locking read becomes a distinctly named method
(`ReadChainHeadForUpdate`), so the unsafe form cannot be reached by forgetting an
argument; choosing the non-locking read is a visible, reviewable act. Plus a
tree-derived sweep proving no production file calls the non-locking read and
every append path takes the lock.

## Class status — CLASS CLOSED

No sibling instances. Enforcement is now structural.

## Evidence confidence — ECL-5 (from ECL-2)

Independent reproduction of the original evidence, whole-tree completeness,
structural enforcement (a distinct method name rather than a flag), mutation
verified, self-validation verified.

## Mutation verification and self-validation

| Control | Result |
|---|---|
| Appender uses the non-locking read (false negative) | fails with **two** diagnostics naming `audit_appender.go` |
| Correct code, short name is a substring of the long one (false positive) | **passes** — the sweep strips `ReadChainHeadForUpdate(` before matching `ReadChainHead(` |
| Break the appender detector (vacuous pass) | fails: *"no audit append path found; the sweep would be vacuous"* |

**Self-validation finding in this audit's own test.** The first version of the
attack ran 12 goroutines and asserted that fewer than 12 committed — a
*probabilistic* assertion that would fail spuriously if the scheduler serialised
them, and which is the likeliest cause of a transient full-suite failure observed
during this audit. It was rewritten to step two transactions by hand, so the
outcome is exact. A second flaw was found and fixed in the process: creating the
genesis head inside both transactions deadlocks them on the unique key, which is
the genesis race rather than the invariant under test; the head is now seeded and
committed first. A verification that depends on timing is a verification defect.

## Residual risk

- The non-locking `ReadChainHead` still exists for read-only verification. The
  sweep forbids it in production files; a future *test-only* helper promoted into
  production would be caught, but the method remains callable in principle.
- ADR-AUDIT-004's boundary with ADR-AUDIT-003 remains UNRECOVERABLE. This EVR
  validates the *behaviour*; it cannot repair the corpus attribution, which
  belongs to the constitutional process.
- The genesis race is fail-closed and retryable, unchanged from
  ADR-CONCURRENCY-004's dossier.
