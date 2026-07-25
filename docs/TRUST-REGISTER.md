# Trust Register

The running ledger of **vulnerability classes eliminated from the running
service**. An entry is admitted only when the class was proven exploitable
against the live system, remediated, re-attacked live to show the exploit now
fails, and locked shut by a test that fails the build if the class returns. Each
entry cites the ADR that carries the full evidence.

The rule this register enforces: eliminate the *class*, not the instance. A fix
that closes one path but leaves the category open is not an entry here.

Legend for **Enforced by**: `arch` = build-failing architecture test; `int` =
integration test against a real PostgreSQL; `unit` = unit test; `startup` =
fail-closed startup validator.

| # | Vulnerability class | Eliminated by | Live proof | Enforced by | ADR |
|---|---|---|---|---|---|
| 1 | Cross-tenant idempotency-key confusion (one tenant's key resolves another's request) | Keys scoped to the supplying tenant | ✓ | int | — (`8ed1e46`) |
| 2 | Platform/tenant administration conflated (a tenant reaching platform administration) | Separate platform vs. tenant admin boundary | ✓ | arch, int | ADR-AUTHZ-001 |
| 3 | Metrics listener exposing the app surface | `/metrics` bound to its own listener | ✓ | arch | — (`8596e4b`) |
| 4 | Cross-tenant event relay (relay delivering across tenant boundaries) | Tenant-confined fan-out in the relay | ✓ | int | ADR-TENANCY-003 |
| 5 | Privileged-worker ownership drift (each worker trusting the queued row's own label) | One ownership framework, re-derived from the audit log | ✓ | arch, int | ADR-TENANCY-003/004 |
| 6 | Confused-deputy policy execution (a policy run against another tenant's event) | Ownership re-derived and authorized at execution, not production | ✓ | int | ADR-TENANCY-003 |
| 7 | Replay asserting authority (replay writing outside its lane) | Replay owns no authority; verification-only | ✓ | int | ADR-TENANCY-005 |
| 8 | Restore-inconsistency (an inconsistent backup silently trusted) | Recovery is a verification boundary; refuse inconsistent restores | ✓ | int | ADR-TENANCY-006 |
| 9 | Ownership-model schema weakening (a migration dropping an invariant the model needs) | Schema invariants validated at startup, fail closed | ✓ | startup, int | ADR-TENANCY-007 |
| 10 | Operator audit-guard removal / untrusted operational tooling | Operational tooling brought inside the trust model | ✓ | int | ADR-TENANCY-008 |
| 11 | Split-brain audit authority (chain head ambiguity) | Audit ownership verified against its root; fail closed on ambiguity | ✓ | int | ADR-AUDIT-004 |
| 12 | Multi-replica double-execution (every replica running the singleton workers) | Leader election on a PostgreSQL advisory lock | ✓ | arch, int | ADR-CONCURRENCY-001 |
| 13 | Concurrent-boot migration race (duplicate-key on `schema_migrations`) | Migrations serialised on a dedicated advisory lock | ✓ | int | ADR-CONCURRENCY-001 |
| 14 | Concurrent double-claim of a queue row (plain-SELECT claim; overlap double-send) | Atomic compare-and-set claim with a lease (`FOR UPDATE SKIP LOCKED`) | ✓ | int | ADR-CONCURRENCY-002 |
| 15 | **Non-dedup-able duplicate delivery** (random ids; a re-processed event becomes a new row with a new dedup key) | **Content-derived, idempotent production** (`DeliveryID`/`ExecutionID`); re-production collides on the primary key | ✓ | arch, int, unit | **ADR-CONCURRENCY-003** |
| 16 | **Demoted leader keeps running its workers** (lock-loss only logged; permanent two-leader overlap) | **Leadership context cancelled on lock loss; re-enters the election** | ✓ | int | **ADR-CONCURRENCY-003** |

## Guarantees stated, not overstated

The register records what was *eliminated*. Two properties are deliberately
*bounded, not removed*, and are stated as such wherever they are relied upon:

- **Delivery and execution are at-least-once, never exactly-once.** Exactly-once
  delivery of an outbound HTTP request is impossible (two-generals); it was
  disproved live and is not claimed anywhere (ADR-CONCURRENCY-002). The remaining
  duplicate is the crash between the outbound action and persisting its result —
  bounded by the claim lease, and dedup-able because every attempt of a logical
  delivery now carries the *same* stable `X-OneOps-Delivery` (ADR-CONCURRENCY-003
  made that key stable across re-production, which is what turns at-least-once into
  effectively-once for a compliant receiver).
- **The leadership step-down window is bounded, not zero.** Up to the health-watch
  interval, a demoted leader may still run its workers; that overlap is made
  *safe* by idempotent production and the atomic claim, not eliminated
  (ADR-CONCURRENCY-003). True fencing of an in-flight outbound call is unclaimed
  future work.

## How to add an entry

Do not add a row until all five hold, or the investigation stays OPEN:

1. the class was exploited against the running service (live evidence);
2. it was remediated to eliminate the class, not the one path;
3. the same exploit was re-run live and now fails;
4. a build-failing test (arch/int/unit/startup) reproduces the regression and
   is shown to bite;
5. the reasoning is written in an ADR and the honest guarantee is not overstated.
