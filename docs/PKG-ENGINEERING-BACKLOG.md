# Platform Knowledge Graph — Engineering Execution Backlog

**Execution artifact.** Traces to the PKG Implementation Specification (`PKG-IMPLEMENTATION-SPEC.md`).
No architecture. No governance. Every item produces code, tests and evidence.

---

## 1. Executive Summary

One epic, six features, nineteen stories, six sprints. **No story is blocked.**

S2.2 (E2 route extractor) was previously blocked on spec §0.2. That blocker is
closed by **Amendment A2** (existing authority: `server.go:149-152`). The route
inventory is derived inside `internal/httpapi` and exported for
`internal/kg/extract/route` to consume; no AST route inventory may exist.

Spec §0.1 and §0.3 are constraints with prescribed actions, executable as
Sprint 0 work.

---

## 2. Epic Breakdown

| ID | Epic | Spec | Outcome |
|---|---|---|---|
| **E-PKG** | Platform Knowledge Graph | whole spec | Architectural knowledge derived from the repository; three EOS manual rows retired |

## 3. Feature Breakdown

| ID | Feature | Parent | Spec | Sprint | Blocked by |
|---|---|---|---|---|---|
| F0 | Repository constraint clearance | E-PKG | §0.1, §0.3 | S0 | — |
| F1 | Core graph + storage + E1 | E-PKG | §II, VII, III(E1), XI/M1 | S1 | F0 |
| F2 | Derived extractors | E-PKG | §III(E3–E8), XI/M2 | S2 | F1 |
| F3 | Validation + confidence engine | E-PKG | §V, VI, XI/M3 | S3 | F2 |
| F4 | CLI + query + output | E-PKG | §VIII, XI/M4 | S4 | F3 |
| F5 | CI integration + freshness | E-PKG | §IX, XI/M5 | S5 | F4 |
| F6 | Text extractors + declarations | E-PKG | §III(E9–E11), XI/M6 | S5 | F3 |

---

## 4–5. Story & Task Breakdown

Every story carries: **Repo area · Deps · Effort (placeholder) · Risk · AC ·
Rollback · Evidence**.

### F0 — Sprint 0 · Constraint clearance

**S0.1 — Register the `kg` binary**
Area `internal/arch/wiring_test.go` · Deps none · Effort XS · Risk **Low**
Files modified: `wiring_test.go` (add `"kg"` to `registeredBinaries`).
Files created: `cmd/kg/main.go` (stub printing version, exit 0).
AC: `go test ./internal/arch/ -run TestOperationalBinariesAreRegistered` passes with `cmd/kg` present.
Rollback: delete both edits. Evidence: guard green with a second binary.
*Trace: spec §0.1 · constraint `registeredBinaries` has one entry.*

**S0.2 — Extractor guard-safety harness**
Area `internal/kg/`, `testdata/kg/` · Deps S0.1 · Effort S · Risk **Med**
Creates: `internal/kg/doc.go`, `testdata/kg/fixtures/` with `.sql` data files.
Adds a package-local test asserting **no §6.2 or audit table name appears as a
literal in any non-test `.go` file under `internal/kg`**, and that no type
declares `Run(ctx context.Context) error`.
AC: all **nine** tree-sweeping guards pass with `internal/kg` present.
Rollback: delete package. Evidence: guard run with the new package in-tree.
*Trace: spec §0.3 · constraint: 9 guards sweep via `goFilesUnder(t, "..")`.*

**Hazard:** this story exists because the extractors describe the very patterns
the guards hunt. If S0.2 is skipped, F2 fails the build in a way that looks like
a guard defect rather than a naming collision.

### F1 — Sprint 1 · Core graph

**S1.1 — `model` + `graph` packages**
Area `internal/kg/model`, `internal/kg/graph` · Deps S0.2 · Effort M · Risk Low
Interfaces: `Node`, `Edge`, `Graph`, `Evidence`, `Origin`, `Confidence` (spec §II verbatim).
Adds `graph.Validate()` enforcing: evidence present unless declared · endpoint existence · ID uniqueness · sorted collections.
Tests: unit + **property test — two builds byte-identical**.
AC: invariants enforced; `model` imports stdlib only; `graph` imports stdlib and
`model`; neither imports any platform package. Rollback: delete packages. Evidence: golden `Graph` fixture.

**S1.2 — `storage` package**
Area `internal/kg/storage` · Deps S1.1 · Effort S · Risk Low
Read/write `pkg.json`, 2-space indent, trailing newline, sorted keys; refuse a
newer `SchemaVersion`. AC: round-trip byte-stable. Evidence: golden file.

**S1.3 — E1 package extractor**
Area `internal/kg/extract/gopkg` · Deps S1.1 · Effort M · Risk Low
`go list -json ./...` → package nodes + import edges. `go list` non-zero is fatal.
AC: node count equals `go list ./... | wc -l`; deterministic across runs.
Evidence: golden graph. Perf: < 2 s.

**S1.4 — `kg build` skeleton**
Area `cmd/kg`, `internal/kg/pipeline` · Deps S1.2, S1.3 · Effort S · Risk Low
AC: `make graph` writes `pkg.json`; exit 0. Rollback: revert; nothing consumes it.

### F2 — Sprint 2 · Derived extractors

| Story | Extractor | Area | Risk | AC |
|---|---|---|---|---|
| S2.1 | E3 schema | `extract/schema` | Med | every table in migrations is a node; unparsed statement warns, does not fail |
| S2.2 | E2 route | `internal/httpapi` (inventory) + `extract/route` (consumer) | Med | route count matches `contract_test.go`'s derivation; no AST route list exists |
| S2.3 | E4 migration | `extract/migration` | Low | lineage ordered; missing rollback pair recorded as attr |
| S2.4 | E5 guard | `extract/guard` | Med | 56 guard nodes; anti-vacuity attr present |
| S2.5 | E6 worker/queue | `extract/worker` | Med | 8 workers, 3 queues, claim sites located |
| S2.6 | E7 openapi | `extract/openapi` | Low | operation count matches `contract_test.go`'s derivation |
| S2.7 | E8 pipeline | `extract/pipeline` | Low | 9 CI jobs, 22 make targets |

Each: golden test + one malformed-input fixture asserting the declared failure
mode. Each independently revertible by removing it from the registry.

### F3 — Sprint 3 · Validation

**S3.1 — Confidence scorer** · Area `internal/kg/pipeline` · Risk **Med**
Origin→max-confidence table (spec §V). `scorer.Validate()` returns error when
`origin=="inferred" && confidence=="certain"`; pipeline aborts.
AC: rule violation aborts the build. Evidence: unit test per origin.

**S3.2 — Validators** · Area `internal/kg/validate` · Risk Low
Ten validators (spec §VI). Only `confidence-violation`, `broken-reference`,
`graph-freshness` are `error`; others `warn`.
AC: each validator has a fixture that triggers it.

**S3.3 — Mutation suite** · Area `internal/kg/` tests · Risk **Med**
Mutants: remove the inferred≠certain rule · drop a validator · break sorting ·
skip an extractor. **Each must fail the build.**
AC: 4/4 killed, or a survivor proven equivalent per Constitution Law 14.4.

### F4 — Sprint 4 · CLI
**S4.1** `query` package + selector syntax *(implementation-specific; engineer decides)* · **S4.2** `output` renderers (human table + `--json`) · **S4.3** the eight commands with exit codes exactly per spec §VIII.
AC: every command's exit code asserted by test.

### F5 — Sprint 5 · CI + text extractors
**S5.1 — Regeneration + determinism test** · Area `internal/kg/pipeline` · Risk **Med**
Build the graph and validate the regeneration in CI; assert determinism by
building twice and comparing the two generated results. Add `pkg.json` to
`.gitignore` (ADR-PKG-001).
AC: a validation `error` fails CI; two builds are byte-identical; `pkg.json` is
absent from the index. **The graph is self-maintaining because it is never
stored — a file that is never committed cannot go stale.**

**S5.2 — `make graph-check` into `build-test`** · no new CI job · Perf budget < 10 s asserted by benchmark.

**S5.3 — E9 ADR extractor** · Risk **High** — spec marks ADR→artifact edges
`Confidence=medium` (regex over prose). **Validate against ten ADRs by hand
before completing.** AC: 30/37 structured headers parsed; citations resolve or
are reported broken.

**S5.4 — E10 doc · S5.5 — E11 declarations + `.pkg/owners.yaml`**
AC: `unknown-owner` reports **18 packages** on first run.

---

## 6. Dependency Graph

```
S0.1 → S0.2 → S1.1 → S1.2 ┐
                  └→ S1.3 ┴→ S1.4 → F2(S2.1,S2.3…S2.7 ∥) → S3.1 → S3.2 → S3.3
                                                                      └→ F4 → S5.1 → S5.2
                                                              S3.2 → S5.3 ∥ S5.4 ∥ S5.5
```
**Critical path:** S0.1 → S0.2 → S1.1 → S1.3 → S1.4 → S2.x → S3.1 → S3.2 → S5.1.
**Parallel:** all F2 extractors after S1.4; S5.3/S5.4/S5.5 after S3.2.
**Merge-order constraint:** S0.1 and S0.2 must merge **before any F2 code**, or
nine guards fail. **CI-order:** S5.2 merges only after S5.1 is green locally.

## 7. Sprint Plan

| Sprint | Content | Independently releasable |
|---|---|---|
| **S0** | S0.1, S0.2 | Yes — a registered stub binary and a guard-safe package |
| **S1** | S1.1–S1.4 | Yes — `pkg.json` with packages; nothing consumes it |
| **S2** | S2.1–S2.7 | Yes |
| **S3** | S3.1–S3.3 | Yes — validation advisory except three rules |
| **S4** | S4.1–S4.3 | Yes — CLI only |
| **S5** | S5.1–S5.5 | Yes — graph becomes self-maintaining |

## 8. Risk Register

| ID | Constraint | Hazard | Failure mode | Detection | Mitigation | Rollback |
|---|---|---|---|---|---|---|
| R1 | `registeredBinaries` = 1 entry | `cmd/kg` fails build | Guard red on first commit | S0.1 | Register in same commit | Delete `cmd/kg` |
| R2 | 9 guards sweep the tree | Extractor literals read as real SQL | Guard red, looks like guard defect | S0.2 test | Derive names at runtime; fixtures as data files | Delete package |
| R3 | §0.2 | *(closed by Amendment A2 — existing authority)* | — | — | — | — |
| R4 | Go map ordering | Non-deterministic graph | Freshness test flaps | S1.1 property test | Sort before emit | Revert extractor |
| R5 | ADR prose citations | E9 quality unknown | Broken-reference noise | S5.3 manual check on 10 ADRs | Keep `medium`; warn-only | Drop E9 |
| R6 | `contract-breaking` is PR-only | Graph changes bypass on direct push | Undetected drift | S5.2 in `build-test` | Freshness in the always-run job | Remove target |
| R7 | Perf budget 10 s | Extraction slows CI | Build-time regression | Benchmark | Concurrent extractors | Drop slowest extractor |

## 9. Traceability Matrix

| Spec § | Backlog | | Spec § | Backlog |
|---|---|---|---|---|
| §0.1 | S0.1 | | §VI | S3.2 |
| §0.2 | S2.2 *(closed by A2)* | | §VII | S1.2 |
| §0.3 | S0.2 | | §VIII | S4.1–S4.3 |
| §I | S1.1, S0.2 | | §IX | S5.1, S5.2 |
| §II | S1.1 | | §X | tests within every story |
| §III E1 | S1.3 | | §XI | Sprint Plan |
| §III E2 | S2.2 | | §XII | §10 below |
| §III E3–E8 | S2.1, S2.3–S2.7 | | | |
| §III E9–E11 | S5.3–S5.5 | | | |
| §IV | S1.4, S3.1 | | | |
| §V | S3.1 | | | |

**No orphan specification sections. No orphan backlog items.**

## 10. Definition of Done — per story

Build passes · tests pass · **all 56+ architecture guards pass** · mutation
tests pass where the story adds a rule · regeneration + determinism validation passes (from S5.1)
· golden tests pass · `pkg.json` regenerated, git-ignored, never committed
(ADR-PKG-001) ·
rollback executed at least once in a non-production environment ·
no `origin=inferred, confidence=certain` node exists.

---

*Execution artifact. Amend freely. Architecture is fixed by the approved spec.*
