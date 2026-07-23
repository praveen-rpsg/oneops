# M4 — Milestone Engineering Plan

## Completing the Governed Operation Set (§8) on the Delivered Event Substrate

| | |
|---|---|
| **Milestone** | M4 — Validation Engine |
| **Goal** | Every one of the twelve §8 Configuration Operations is executable through the single atomic governance path and emits a complete, verifiable event trail |
| **Repository at planning** | branch `release/v1.0.0`, HEAD `9032021`, tag `v1.0.0` |
| **Planner role** | Principal Platform Engineer / Repository Maintainer |
| **Date** | 2026-07-23 |

Every work package traces to a ratified decision, an accepted ADR/RFC, or measured repository evidence. No package introduces architecture.

---

# 1. Current Repository Assessment

## 1.1 Completed capabilities (measured)

| Milestone | Delivered | Evidence |
|---|---|---|
| **M1** | Configuration Registry — CRUD, bulk, idempotency, ETag/If-Match, keyset pagination, RBAC, OpenAPI | tag `v0.1.0-m1` |
| **M2** | Dependency graph — `dependency_edge` (`depends`/`extends`/`supersedes`), cycle-safe recursive-CTE traversal, REST | tag `v0.2.0-m2` |
| **M3.1–M3.5** | Authority Resolver — **all four Replacement-Test conjuncts implemented**: `activedeps.go`, `responsibility.go`, `citation.go`, `gap.go` | commits `90574e5`→`15b3c5f` |
| **v1.0.0 GA** | Hash-chained audit + verifier + anchors, event delivery (relay/dispatcher/replay/retention/HMAC signing), policy automation, execution timeline, compliance evidence export | tag `v1.0.0`, HEAD `9032021` |

## 1.2 The measured gap — §8 operation coverage

`internal/domain/audit.go` defines **all twelve** `ConfigurationOperation` constants, and all twelve pass `Valid()` — the audit layer accepts every one. `internal/governance/transition.go::planTransition` implements **seven**.

| §8 Operation | Constant | `planTransition` | Schema ready? |
|---|---|---|---|
| Ratification | ✅ | ✅ implemented | ✅ |
| Approval | ✅ | ✅ implemented | ✅ |
| **Extension** | ✅ | ❌ `ErrUnsupportedOperation` | **✅ `extends` edge exists (M2)** |
| **Replacement** | ✅ | ❌ `ErrUnsupportedOperation` | **✅ `supersedes` edge (M2) + 4-conjunct test (M3)** |
| Suspension | ✅ | ✅ implemented | ✅ |
| Deprecation | ✅ | ✅ implemented | ✅ |
| Withdrawal | ✅ | ✅ implemented | ✅ |
| Archiving | ✅ | ✅ implemented | ✅ |
| **Historical Preservation** | ✅ | ❌ `ErrUnsupportedOperation` | standing guarantee — no schema expected |
| Deletion | ✅ | ✅ implemented | ✅ |
| **Baseline Freeze** | ✅ | ❌ `ErrUnsupportedOperation` | ❌ needs immutable-set schema |
| **Amendment** | ✅ | ❌ `ErrUnsupportedOperation` | ❌ needs version lineage |

**The headline finding:** M3 built a complete four-part Replacement Test evaluator, and **the operation that consumes it does not exist.** A finished evaluator with no caller is the single most valuable, most-ready piece of work in the repository — and it needs **no schema change**.

## 1.3 Technical debt — documented technology divergence

The Execution Playbook specifies M4 as *"Orchestrate runs (Temporal) + events — workflow, NATS streams, outbox/inbox."* Measured:

- **Zero Go files import NATS, Temporal, Redis or OpenSearch.** `go.mod` direct dependencies are chi, golang-jwt, pgx, ulid, prometheus, otel — nothing else.
- `docker-compose.yml` declares `nats`, `redis`, `opensearch`, `minio` services that **no application code uses**.
- The delivered substrate is instead **audit-log-as-outbox**: the relay tails the committed `audit_event` log, so an event is delivered iff its operation committed (`ADR-AUDIT-005`). This has a property a separate outbox table lacks — there is no second write to keep consistent and therefore no dual-write hazard.

**Disposition.** Repository evidence is the source of truth until documentation is corrected. The divergence is an improvement, not a defect, but it is currently *undocumented*, which makes it accidental rather than decided. WP-7 records it. This does **not** require the Amendment Process: the M1 Constitutional Court ruling holds that *the Engineering Execution Playbook is not a constitutional authority* and milestone acceptance guidance is GOV-1 governance guidance, not constitutional law. An ADR is the correct instrument.

Secondary debt: four unused compose services are misleading to a new engineer. Removing them is in scope for WP-7 as documentation-truth work, not infrastructure work.

## 1.4 Governance artifacts

| Artifact | State |
|---|---|
| **Accepted RFCs** | RFC-001 (dependency-rule enforcement) — Accepted |
| **Accepted ADRs** | ADR-ARCH-001 (dependency-rule enforcement) — Accepted |
| **Referenced but unwritten ADRs** | `ADR-AUDIT-003/004/005` — rationale lives only in code comments (open under `EA-20`) |
| **Open EAs relevant to M4** | EA-20 (ADR back-fill). EA-02 **closed** by ADR-ARCH-001 |

## 1.5 Repository health

| Check | Result |
|---|---|
| `go build ./...` · `go vet ./...` · `gofmt` | clean |
| Full suite `-race` | **all green** |
| golangci-lint 2.12.2 | **0 issues** |
| Total coverage | **61.9%** |
| Architecture rules (`internal/arch`) | **4/4 pass** |
| Working tree | untracked additions only (`docs/`, `internal/arch/`); **nothing committed** |
| Branch | `release/v1.0.0` — **M4 must branch from a new integration branch, not this one** |

---

# 2. Milestone Scope

## 2.1 Objective

Make all twelve §8 Configuration Operations executable through the single atomic governance mutation path, each emitting a complete and independently verifiable event trail.

## 2.2 Success criteria

1. `planTransition` returns a valid plan for **all twelve** operations; `ErrUnsupportedOperation` is unreachable for any §8 operation.
2. **Replacement** executes only when the four-part Replacement Test passes, consuming the M3 evaluators unchanged.
3. **Extension** leaves the base `Active` and sets *Extended By* — the CVP error is structurally impossible.
4. Every operation commits governance state and its audit append **in one transaction** (`ADR-AUDIT-005`, unchanged).
5. Every committed operation produces an event trail: audit event → relay → signed delivery, **at-least-once with zero duplicate effects**.
6. CI green: build, unit, integration (fail-on-skip), migrations, lint, security, architecture rules.

## 2.3 Out of scope

- **Temporal, NATS, Redis, OpenSearch.** Not adopted; not adopted by this milestone (WP-7 records why).
- **A generic validation-run engine.** `ops.Scheduler` already provides trigger→verdict for audit integrity. Generalizing it for M5's benefit is future work and is excluded by the hard rules.
- **M5 Conformance rule engine**, Golden Runner, portal.
- **ADR-AUDIT back-fill** (EA-20) — tracked, not bundled.
- **Any change to the M3 authority evaluators.** They are consumed as-is; a required change to them is a defect signal, not scope.
- **Any change to the audit canonical form.** Frozen (Guide §8.3).

## 2.4 Dependencies

| Dependency | State |
|---|---|
| M1 Registry, M2 graph (`extends`/`supersedes` edges) | ✅ delivered |
| M3 Replacement Test — 4 conjuncts | ✅ delivered |
| Audit chain + event delivery + replay | ✅ delivered (v1.0.0) |
| `ADR-AUDIT-005` atomic mutation | ✅ in force |

**M4 has no unmet dependency.** Every prerequisite shipped.

## 2.5 Required RFCs / ADRs / migrations

| Required | Item |
|---|---|
| **RFCs** | RFC-002 (Baseline Freeze + Amendment schema — WP-3/WP-4 only) |
| **ADRs** | ADR-ARCH-002 (event/orchestration substrate divergence); ADR-GOV-001 (Replacement operation binds the M3 test) |
| **Migrations** | One forward migration for WP-3/WP-4 (`baseline_set` + version lineage), with rollback script. WP-1, WP-2, WP-5 need **none** |

---

# 3. Work Breakdown Structure

Each package is independently reviewable and independently revertable.

### WP-1 — Replacement operation *(highest value, zero schema)*

- **Purpose:** implement §8 Replacement by binding the M3 four-part Replacement Test to `planTransition`. Base → `Historical`, Retention → `Historical Record`, *Superseded By* set.
- **Files:** `internal/governance/transition.go`, `engine.go` (successor resolution), `internal/governance/replacement.go` *(new)*
- **Packages:** `governance` gains a port satisfied by the existing `authority` evaluators. **`internal/authority` is not modified.**
- **Interfaces:** `type ReplacementTester interface { Evaluate(ctx, baseID, successorID) (domain.ReplacementResult, error) }` — declared by `governance`, consumer-side (Guide §2.3)
- **Tests:** unit — all four conjuncts pass → Historical; each conjunct failing independently → rejected with the specific reason; integration — atomic commit + audit append
- **Verification:** V1, V2, V4, V6 (§5)
- **Complexity:** **M** — logic exists; this is wiring plus precondition enforcement
- **Depends on:** nothing new

### WP-2 — Extension operation *(zero schema)* — ✅ **IMPLEMENTED 2026-07-23**

> **Status:** complete. `planTransition` handles `OpExtension`; `POST /v1/governance/{id}/extend` live; `ADR-GOV-001` merged. Build/vet/gofmt/lint clean, full `-race` suite green, coverage unchanged at 61.9%, `internal/authority` 0-diff. **8 of 12 §8 operations now implemented** (was 7). Deferred to WP-1: the "responsibilities not re-owned" precondition, which requires the M3.3 evaluator — see ADR-GOV-001 §3.

- **Purpose:** implement §8 Extension. **Base Authority stays `Active`**; *Extended By* += successor. This is the operation whose absence caused the CVP error.
- **Files:** `internal/governance/transition.go`, `engine.go`
- **Interfaces:** reuses the M2 `extends` edge; no new port
- **Tests:** base remains `Active` after extension (**the anti-CVP regression test**); base responsibilities not re-owned; extension ≠ replacement
- **Verification:** V1, V2, V4, V6
- **Complexity:** **S**
- **Depends on:** nothing

### WP-3 — Baseline Freeze *(schema)*

- **Purpose:** implement §8 Baseline Freeze — a ratified set declared immutable; sets the immutable flag, Authority `Active`, Lifecycle `Ratified`.
- **Files:** migration + rollback, `internal/domain/baseline.go`, `internal/store/postgres/baseline_store.go`, `transition.go`
- **Interfaces:** `BaselineStore` port declared by `governance`
- **Tests:** freeze marks the set immutable; **post-freeze mutation of a member is refused**; unfreeze is not an operation
- **Verification:** V1–V6, plus migration validation
- **Complexity:** **L**
- **Depends on:** RFC-002 accepted

### WP-4 — Amendment *(schema)*

- **Purpose:** implement §8 Amendment — new ratified version; prior version → `Historical Record`; *Superseded By* set. Enforces the four-condition Amendment Rule as a **precondition on recorded evidence**, not as a judgment the engine makes.
- **Files:** migration + rollback (version lineage), `transition.go`, `engine.go`
- **Tests:** amendment without the four-condition evidence is **refused**; prior version transitions to Historical; lineage is queryable
- **Verification:** V1–V6
- **Complexity:** **L**
- **Depends on:** WP-3 (shares the migration), RFC-002

### WP-5 — Historical Preservation

- **Purpose:** implement §8 Historical Preservation as the standing guarantee it is: no deletion of anything with historical value or dependents, Rule-2 disclaimer applied.
- **Files:** `transition.go`, deletion-guard tests
- **Tests:** deletion refused for Constitution/Governance/Validation/Evidence/Audit roles and for anything with dependents (§8 Deletion row)
- **Verification:** V1, V2, V6
- **Complexity:** **S** — likely an invariant assertion plus a guard, no schema
- **Depends on:** nothing

### WP-6 — Operation → event-trail conformance

- **Purpose:** prove the milestone exit criterion on the **delivered** substrate. Not new infrastructure — an end-to-end conformance test that every one of the twelve operations produces audit → relay → signed delivery, at-least-once, zero duplicate effects.
- **Files:** `internal/httpapi/*_integration_test.go`, `internal/events/*_test.go`
- **Tests:** per operation — committed audit event exists; relay delivers exactly the committed set; **duplicate delivery is a no-op** (idempotent consumer); rollback emits nothing
- **Verification:** V2, V3, V6
- **Complexity:** **M**
- **Depends on:** WP-1…WP-5

### WP-7 — Substrate divergence recorded

- **Purpose:** convert an accidental divergence into a decided one. `ADR-ARCH-002` records that Temporal/NATS were not adopted and that audit-log-as-outbox supersedes them, with the dual-write rationale. Remove the four unused `docker-compose` services or comment them as unused.
- **Files:** `docs/adr/ADR-ARCH-002-*.md`, `docker-compose.yml`, `README.md`
- **Tests:** none (documentation + compose)
- **Verification:** review
- **Complexity:** **S**
- **Depends on:** nothing — **do this first**, it legitimizes the milestone's shape

## 3.1 Dependency graph

```
WP-7 (substrate ADR) ──┐  [do first; unblocks nothing technically, legitimizes scope]
                       │
WP-1 (Replacement) ────┤
WP-2 (Extension) ──────┼──► WP-6 (event-trail conformance) ──► EXIT
WP-5 (Hist. Pres.) ────┤
                       │
RFC-002 ──► WP-3 (Freeze) ──► WP-4 (Amendment) ──┘
```

**Critical path:** RFC-002 → WP-3 → WP-4 → WP-6. **WP-1, WP-2, WP-5, WP-7 are parallel and unblocked today.**

Recommended first commit: **WP-2** (smallest, zero schema, carries the anti-CVP regression test), then **WP-1** (highest value).

---

# 4. RFC Matrix

| WP | RFC status | Justification |
|---|---|---|
| **WP-1** Replacement | **No RFC required** | Implements a ratified operation (State Model §8) using delivered components; no new architecture, no schema. Needs **ADR-GOV-001** to record that the operation binds the M3 test as precondition |
| **WP-2** Extension | **No RFC required** | Ratified operation, no schema, no new dependency |
| **WP-3** Baseline Freeze | **Needs RFC-002** | Introduces schema (immutable set) and an irreversible semantic. Guide §7.7 + §10.5 make schema on truth-bearing tables an architecture-review item |
| **WP-4** Amendment | **Covered by RFC-002** | Shares the migration and lineage model. **Do not raise a second RFC** |
| **WP-5** Historical Preservation | **No RFC required** | Enforces an existing standing guarantee; no schema |
| **WP-6** Event-trail conformance | **No RFC required** | Tests only; asserts delivered behavior |
| **WP-7** Substrate divergence | **No RFC required** | Retrospective record of a delivered decision → **ADR-ARCH-002** |

**RFCs required: exactly one (RFC-002), covering WP-3 and WP-4.**
**ADRs required: two — ADR-ARCH-002 (WP-7), ADR-GOV-001 (WP-1).**

---

# 5. Verification Matrix

| # | Verification | Applies to | Objective acceptance |
|---|---|---|---|
| **V1** | Compilation | all | `go build ./...`, `go vet ./...` (incl. `-tags integration`) clean |
| **V2** | Unit | WP-1…WP-6 | Every operation: success path + **each precondition failure independently**. ≥80% on changed packages |
| **V3** | Integration | WP-1, WP-3, WP-4, WP-6 | Real Postgres, `-tags integration`. Atomic commit proven; **CI fails on any skip** |
| **V4** | Architecture | all | `internal/arch` 4/4 pass. `governance` declares its own ports; `internal/authority` unmodified (`git diff --stat` = 0) |
| **V5** | Migration | WP-3, WP-4 | `atlas migrate validate` green; rollback script present and exercised; expand→migrate→contract respected |
| **V6** | Backward compatibility | all | Existing 7 operations byte-identical in behavior; M1/M2/M3 test files **0-diff**; `/v1` additive only; SDK regenerated |
| **V7** | Performance | WP-1, WP-6 | Governance mutation p95 **< 250 ms** (Guide §10.6); event relay lag p95 **< 5 s** |
| **V8** | Security | WP-3, WP-4 | Freeze/Amendment require `artifacts:admin`; denial tested; no secret in logs |
| **V9** | CI | all | build-test, integration, migrations, lint, security, docker — **all green** |
| **V10** | Audit integrity | all | `POST /v1/admin/integrity/run` → `healthy: true` after every new operation type |

**Non-negotiable:** V6's 0-diff requirement on M1/M2/M3 test files. Every prior milestone was verified against exactly this discipline; M4 does not get an exception.

---

# 6. Risk Register

| # | Risk | Class | Severity | Mitigation |
|---|---|---|---|---|
| **R1** | Replacement wiring subtly changes M3 authority resolution, silently altering computed authority | Architectural | **High** | `governance` consumes `authority` through a consumer-declared port; V4 asserts `internal/authority` is 0-diff. Any required change there halts the WP for architecture review |
| **R2** | Baseline Freeze immutability is enforced in the handler, so a direct repository write bypasses it | Technical | **High** | Enforce in the repository/query layer (Guide §8.5 pattern); negative integration test proves a frozen member cannot be mutated by any path |
| **R3** | Widening the atomic transaction to cover successor-edge writes breaks `ADR-AUDIT-005` | Architectural | **High** | Explicit review gate: WP-1/WP-2 must not enlarge the transaction beyond state + audit append. Guide §3.4 makes this architecture review, not code review |
| **R4** | Amendment's four-condition rule is implemented as engine *judgment* rather than recorded *evidence* | Architectural | Medium | The engine checks that evidence is **present and attributed**; it never adjudicates. Adjudication is the Council's (§8 Authority column) |
| **R5** | Duplicate deliveries produce duplicate effects, failing the at-least-once/0-dup criterion | Operational | Medium | WP-6 tests idempotency per operation keyed on `EventID`; the delivered dispatcher already retries with backoff + dead-letter |
| **R6** | Migration for WP-3/WP-4 is not N−1 compatible, breaking rollback | Operational | Medium | Expand→migrate→contract (Guide §7.7); no destructive statement in the release that stops writing the old shape; rollback rehearsed in staging |
| **R7** | Work is committed to `release/v1.0.0` after `v1.0.0` was tagged | Schedule/Ops | **High** | Branch `feat/m4-governed-operations` from the tag. **No M4 commit lands on the release branch** |
| **R8** | Scope creep into a generic validation-run engine "for M5" | Schedule | Medium | Explicitly out of scope (§2.3). `ops.Scheduler` is not generalized in M4 |
| **R9** | Freeze/Amendment expose an irreversible operation without an authorization gate | Security | **High** | V8: `artifacts:admin` required, denial tested. Freeze has no inverse operation — that is intended and must be documented in RFC-002 |
| **R10** | Playbook divergence stays undocumented, so a future engineer "restores" NATS/Temporal | Architectural | Medium | WP-7 first. ADR-ARCH-002 makes the decision findable |

---

# 7. Exit Criteria

M4 is complete when **every** item is objectively true:

**Work packages**
- [ ] WP-1 Replacement, WP-2 Extension, WP-3 Baseline Freeze, WP-4 Amendment, WP-5 Historical Preservation, WP-6 event-trail conformance, WP-7 substrate ADR — all merged
- [ ] `planTransition` handles **all twelve** §8 operations; `ErrUnsupportedOperation` unreachable for any §8 operation

**Verification**
- [ ] V1–V10 pass (§5)
- [ ] M1/M2/M3 test files **0-diff**; all prior tests pass unchanged
- [ ] `internal/authority` **0-diff**
- [ ] Architecture rules 4/4; coverage not reduced below 61.9%

**Governance**
- [ ] **RFC-002** accepted (WP-3/WP-4) with a change-review record
- [ ] **ADR-ARCH-002** and **ADR-GOV-001** merged
- [ ] Guide updated where measurement corrected it; any new EA registered in Appendix A

**Repository**
- [ ] Clean working tree; all work committed on `feat/m4-governed-operations`, **not** on `release/v1.0.0`
- [ ] CI green on all six jobs
- [ ] `POST /v1/admin/integrity/run` → `healthy: true`
- [ ] Migration validated, rollback script present and rehearsed

**Demonstration** (Playbook M4 demo criterion, on the delivered substrate)
- [ ] Trigger a Replacement on a base with a passing four-part test → base flips to `Historical`, audit event committed, signed webhook delivered, timeline reflects it, compliance export is deterministic

---

## Appendix — Planning Measurements

```bash
export PATH=/opt/homebrew/bin:$PATH GOTOOLCHAIN=local

git log --oneline -5                                  # HEAD 9032021, tag v1.0.0
grep -c "case domain.Op" internal/governance/transition.go   # 7 implemented
grep "Op[A-Z].*ConfigurationOperation" internal/domain/audit.go | wc -l  # 12 defined
grep -ril "nats\|temporal\|opensearch\|redis" --include="*.go" .         # 0 files
grep "ck_edge_kind" internal/store/migrate/sql/20260722000002_graph.sql  # extends/supersedes present
ls internal/authority/                                # activedeps, responsibility, citation, gap
go build ./... && go vet ./... && go test ./... -race  # all green
```

**Key measured facts driving this plan:** twelve operation constants exist and validate; seven are implemented; the `extends` and `supersedes` edges already exist; the four-part Replacement Test is complete and uncalled; NATS/Temporal were never adopted.
