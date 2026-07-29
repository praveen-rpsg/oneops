# EVR-009 — Remaining entries: 3, 9, 12, 13, 23, 24

| | |
|---|---|
| **Date** | 2026-07-29 |
| **Trust Register entries** | 3, 9, 12, 13, 23, 24 |
| **Confidence** | **PARTIALLY VALIDATED** — all six classes closed; **two entries overstated their enforcement tier** |

This EVR completes the sweep. Every Trust Register entry now has fresh evidence.

## Entry 13 — concurrent-boot migration race · **INSUFFICIENTLY VERIFIED → CLASS CLOSED**

The register listed integration enforcement. **No test exercised the race.** The
migration tests covered pending-detection and schema shape, both single-threaded;
the claim rested on the advisory lock *existing* rather than on evidence it
works.

Missing evidence supplied — six replicas booting simultaneously against an empty
schema, which is when each tries to create `schema_migrations`:

```
6 concurrent boots: 0 failed; schema_migrations holds 15 rows, 15 distinct versions
```

**Negative control (lock removed)** reproduces the original report exactly:

```
replica 0: create schema_migrations: ERROR: duplicate key value violates unique
           constraint "pg_type_typname_nsp_index" (SQLSTATE 23505)
replica 1: … replica 3: … (three of six)
```

ECL-2 → **ECL-4** (fresh evidence, mutation-verified; the mechanism is a single
lock on a single path, so there is no subject set to sweep).

## Entry 3 — metrics listener · **INSUFFICIENTLY VERIFIED → CLASS CLOSED**

The register recorded **`arch`** enforcement. **No architecture test existed.**
The actual protection was a conditional mount plus a production-config check —
real, but not the tier claimed. The register overstated it.

Guard added: the public router may mount `/metrics` only under
`MetricsAddr == ""`, and production config must still refuse an empty
`ONEOPS_METRICS_ADDR` *and* still fail startup on config problems. Mutation:
mounting `/metrics` unconditionally fails the build.

ECL-2 → **ECL-4**.

## Entry 12 — multi-replica double-execution · **CLASS CLOSED**

Enforcement was already structural: an AST sweep of `main.go` for any worker
`Run` launched outside `ops.RunAsLeader`, with `perReplicaSupervisors` as a
justified allowlist. Re-verified passing. EVR-007 added the complementary half —
every worker loop must *observe* the cancellation. ECL-2 → **ECL-4**.

## Entry 9 — ownership-model schema weakening · **CLASS CLOSED**

Superseded in place by ADR-SECURITY-003: `SchemaValidator` is a registered
platform invariant, so it runs at boot **and** continuously, and
`TestEveryValidator_IsRegisteredAsAnInvariant` (now tree-derived) proves no
validator can exist unregistered. ECL-2 → **ECL-5**, maturity L1 → L4.

## Entries 23 and 24 — governance destruction and constitutional inputs · **CLASS CLOSED**

Both were closed by this programme's own recent work with structural guards:
entry 23 by an AST check that the persistence contract exposes no destructive
method (you cannot call what cannot be expressed), entry 24 by enforcement at the
storage chokepoint every metadata writer passes through. Both re-verified
passing. ECL-3 → **ECL-4**.

## Root cause across this EVR

Two entries claimed enforcement they did not have. Neither was a platform defect
— both mechanisms were sound — but both were **evidence defects**: the register
described a stronger tier than existed. Under this programme's law that is itself
an architectural defect, and it is why the sweep was worth completing rather than
stopping once the interesting classes were done.

## Residual risk

- Entry 13's mechanism is a single lock on a single path; there is no subject set
  to derive, so it stays at ECL-4 rather than ECL-5. Adding a second migration
  path would need this evidence re-run.
- Entry 3's guard asserts the conditional mount and the config refusal; it cannot
  prove a deployment actually sets `ONEOPS_METRICS_ADDR`.
- Entries 1 and 3 still have **no ADR**. Their EVRs are the architectural record.
