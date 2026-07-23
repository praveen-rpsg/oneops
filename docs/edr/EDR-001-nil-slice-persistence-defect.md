# EDR-001: Nil slices must be normalized before binding to `text[] NOT NULL`

| | |
|---|---|
| **Type** | Engineering Decision Record — *not* an ADR. No architecture changed. |
| **Date** | 2026-07-23 |
| **Author** | Platform Engineering |
| **Status** | Decided · implemented · released as `1.0.1` |
| **Commits** | `a808544` (fix, 3 files, +87/−3) · `78adc1b` (release metadata, 2 files) |
| **Base** | `9032021` = tag `v1.0.0` |
| **Related** | ADR-GOV-001 (unaffected) · M4 WP-2 (unaffected) |

---

## 1. Executive Summary

While preparing a production-readiness review for an unrelated feature, two integration tests were found failing at the exact commit tagged `v1.0.0`. They were initially classified as pre-existing test breakage unrelated to the feature under review — which was correct as far as it went, and which is where the investigation nearly stopped.

Measuring the failure rather than filing it revealed a **production defect**: two documented default API behaviours returned `HTTP 500` in the GA release. The fix is 23 lines of production code at three call sites, with no schema, API, or dependency change. It shipped as `1.0.1`.

The durable lesson is not about pgx. It is that **a failing test was treated as a test problem before anyone asked what the test was reporting.**

## 2. Original Assumption

**What was believed.** During the WP-2 production-readiness review, `TestWebhookStore_Integration` and `TestTimelineStore_Integration` failed. They were classified as *"pre-existing, unrelated to the change under review, non-blocking for this work package."*

**Who believed it, and why it was reasonable.** The reviewer (Platform Engineering) held this, and the reasoning was sound on the evidence then available:

- The failures were in webhook and timeline persistence; the change under review touched governance. **Different packages, no overlap.**
- The classification was *verified*, not assumed: the same failures were reproduced in an isolated clean-HEAD clone with the feature absent. That measurement was correct and remains correct.
- The repository's own `RELEASE-CHECKLIST.md` at `v1.0.0` carried the integration-suite line as unchecked — `[ ]` — so a known-unverified integration suite was consistent with the recorded state of the release.

**The assumption was accurate and incomplete at the same time.** "Not caused by this feature" was true. "Therefore not urgent" did not follow, and that inference — not the measurement — is where the gap was.

## 3. Repository Evidence

### Measured facts

| # | Measurement | Result |
|---|---|---|
| M1 | Integration suite at HEAD (working tree) | 2 failures, `webhook_replay_job.delivery_ids` NOT NULL violation, SQLSTATE 23502 |
| M2 | Same suite in an isolated clean clone at `9032021` | **Identical failures** → pre-existing, feature-independent |
| M3 | Schema grep for the column class | **3** columns are `text[] NOT NULL DEFAULT '{}'`: `webhook_replay_job.delivery_ids`, `webhook.operations`, `webhook.resources` |
| M4 | Standalone pgx program against real Postgres | `nil []string` → **FAIL 23502**; `[]string{}` → OK; `[]string{"x"}` → OK |
| M5 | Handler read-through (`replayWebhook`, `createWebhook`) | Both pass request slices to the store **uncoalesced**; `omitempty` JSON fields yield nil |
| M6 | Live API at `v1.0.0`, webhook with no filters | **HTTP 500** |
| M7 | Live API at `v1.0.0`, time-window replay | **HTTP 500** |
| M8 | Same two calls with explicit arrays | 201 / 202 — the failure is specific to the omitted-field path |
| M9 | Write-site enumeration | **3** sites bind these columns; scan sites are reads and unaffected |
| M10 | Test corpus review | Every pre-existing test supplied **populated** slices; none exercised nil |
| M11 | Operator-visible output for the 500 | One `INFO http_request status:500` line. **No error log, no cause, no SQLSTATE** |
| M12 | Post-fix integration suite | Green, 0 skips — the first measured green run in this repository |
| M13 | Rollback: `v1.0.0` image against a `1.0.1`-written database | readyz ok, reads ok, audit chain `healthy: true`, old bug reproduces |

### Inference (not measured)

- That no corrupt data exists to migrate. **Derived, not observed**: the insert failed outright, so no row could have been written. Sound, but it is a deduction.

### Rejected assumptions

| Rejected | Why |
|---|---|
| "It's a test bug" | M4–M7: the failing statement is production code on a production path |
| "It only affects replay" | M3 + M6: `webhook.operations`/`resources` share the defect class; webhook creation is the more central path |
| "Nil is an edge case" | M5: nil is the **documented default** — empty means "all events"; absent `delivery_ids` means time-window mode |
| "`DEFAULT '{}'` protects the column" | M4: an explicit `NULL` bypasses a column default. The default only applies when the column is omitted from the INSERT |

### Unknowns at time of decision

- **Benchmark regression status** — the suite exceeded a 10-minute budget twice and was not measured. Unrelated to this change (no benchmarked path touched), but genuinely unknown.
- **gitleaks / trivy** were not re-run. `go.mod` and `go.sum` are byte-identical to `v1.0.0`, so the dependency vulnerability surface cannot have changed — reasoned, not measured.

## 4. Root Cause

**Root cause.** pgx encodes a **nil** Go slice as SQL `NULL`. An explicit `NULL` **bypasses the column `DEFAULT`**, so binding nil to a `text[] NOT NULL DEFAULT '{}'` column violates the not-null constraint. The schema and the Go type system were each individually correct and disagreed at the boundary between them.

**Contributing factors.**

1. **Nil is meaningful at every affected call site.** `events.Webhook.Matches` treats empty Operations/Resources as "match everything"; `events.ReplayJob` treats absent DeliveryIDs as time-window mode. The type system cannot distinguish "absent" from "empty" for a Go slice, so the two meanings collapse onto the same value.
2. **The test corpus never exercised nil (M10).** Every test supplied populated slices — the natural thing to write when demonstrating that filtering works. Coverage percentage did not reveal this, because the *lines* were covered; only the *value* was not.
3. **The failure was unobservable in production (M11).** A 500 with no cause gives an operator nothing to escalate.
4. **The release proceeded with the integration box unchecked.** The checklist recorded this honestly; the release did not stop for it.

**Appeared related, but was not.**

- The `nullTime` helper sits directly above the defective call and handles the analogous nil-time case correctly. Its presence made the surrounding code look like nil-handling had been considered — a coincidence of proximity, not evidence.
- Four unused compose services (NATS, Redis, OpenSearch, MinIO) were discovered nearby. Unrelated; recorded separately.
- The GA release notes claim *"CI now runs PostgreSQL-backed integration tests (fails on skip)."* True as configuration. It says nothing about whether they passed, and was not a false statement.

## 5. Engineering Decision

**Decision: normalize nil to an empty slice at the persistence boundary, via one unexported helper applied at the three write sites.**

**Why this approach.**

- The invariant being protected (`text[] NOT NULL`) belongs to the storage layer, so the correction belongs there.
- Nil carries real meaning at every caller. Normalizing once preserves that meaning for all current and future callers instead of pushing a storage constraint outward into transport.
- Three call sites, one helper, one place to read the explanation.

**Why alternatives were rejected.**

| Alternative | Rejected because |
|---|---|
| Coalesce in the HTTP handlers | Fixes one caller; leaves the store fragile for the other two and for every future caller |
| `COALESCE($n,'{}')` in SQL | Correct, but repeats the workaround in three statements with no shared explanation of *why* |
| Make the columns nullable | A migration that discards a correct constraint to accommodate a client-side bug |
| Change `[]string` to a wrapper type | A type-system change rippling through domain, events, transport and SDK — disproportionate to a three-line defect |

**Why the scope is correct.** The scope grew once, on evidence: from the one column the failing test exposed to all three columns of the same class (M3), because the second and third were the same defect and would otherwise have been rediscovered later. It did **not** grow to the adjacent observability gap (M11), which was recorded and deferred rather than bundled — a production bugfix should not carry a platform-wide refactor.

## 6. Verification

Performed and recorded. Nothing below is claimed that was not run.

| Category | Evidence |
|---|---|
| Build | `go build ./...`, `go vet ./...` and `-tags integration` clean, in an isolated worktree at the commit |
| Format / Lint | `gofmt` clean; golangci-lint 2.12.2 `./...` → **0 issues** |
| Unit | Full suite `-race -count=1` — 17/17 packages ok |
| Integration | Full suite against real Postgres — **green, 0 skips** (M12). Both previously-failing tests pass |
| Regression test | `TestWebhookStore_NilSlicesPersist` covers nil at all three write sites |
| Migrations | `atlas migrate validate` exit 0 — first verified run in the repository's history |
| Container | `docker build` clean; `make docker VERSION=1.0.1` produced an image reporting `version:1.0.1, commit:a808544` |
| Runtime | Both previously-500 paths return 201 / 202 against a running control plane, on the release artifact |
| Rollback | **Executed, not reasoned** (M13): the `v1.0.0` image ran against a database written by `1.0.1`, served reads, verified the audit chain healthy, and still reproduced the old bug — confirming genuine N−1 code |
| Operational | `/healthz`, `/readyz`, `/metrics`, audit integrity run `healthy: true`; negative paths 404 / 409 / 412 / 400 correct |

**Not verified:** benchmark regression (timed out, twice); gitleaks/trivy (not re-run — dependency set is byte-identical to `v1.0.0`).

## 7. Consequences

**Positive.** Two documented default API behaviours work again. The integration suite is green for the first time in the repository's measured history, so it is now a usable signal rather than known-noise. `atlas migrate validate` and the rollback path have been genuinely exercised. A regression test now pins the nil case at all three sites.

**Negative.** Unit-run coverage moved 61.9% → 61.8%: `textArray` is exercised only under `-tags integration`, which the coverage profile excludes — the same pattern as the pre-existing `nullTime` beside it. The helper is genuinely covered; the metric simply cannot see it.

**Deferred.**

| Item | Owner | Reason |
|---|---|---|
| Error-cause logging at the HTTP boundary — 51 of 52 `mapError` sites discard the cause | unassigned | Platform-wide observability change; must not ride on a targeted bugfix. Design reviewed separately: approved with changes |
| Tag `v1.0.0` remains attached to a commit whose integration suite fails | release management | Historical fact; cannot be rewritten |
| Benchmark budget — suite exceeds 10 minutes | unassigned | Needs its own investigation |

**Future considerations.** Any new `text[] NOT NULL` column inherits this hazard; the helper exists and should be used. More generally, the class is *"a Go zero value that is semantically meaningful but encodes to something the schema rejects"* — nil maps, nil slices, and zero times are all candidates. `nullTime` shows the pattern was already understood for one type.

## 8. Lessons Learned

**We originally believed** that two failing integration tests were pre-existing breakage unrelated to the change under review — a conclusion that was measured, reproduced in a clean clone, and correct.

**We discovered** that "unrelated to my change" and "not urgent" are different claims, and only the first had been established. The failing assertion was production code rejecting a production default, and the same defect class sat in two further columns that no test touched.

**This changes future engineering because** a red test at a release commit is now treated as an unclassified production signal until measured, not as an inherited condition. Concretely: a `[ ]` on the release checklist is a blocking unknown, not a note.

**The repository taught us** three things it already knew and we had not asked it. That the schema and the Go type system disagreed at a boundary neither owned. That 100% line coverage over a call site says nothing about the *values* that reach it — every test supplied populated slices, and the gap was invisible to the metric. And that a system which cannot report the cause of its own 500s will hide a defect for an entire release: the fix took under an hour once measured, and the measurement was only possible because someone ran the API by hand and read the log.
