# HEALTH-001 — Engineering Health Audit (Baseline)

| | |
|---|---|
| **Date** | 2026-07-23 |
| **Auditor** | Chief Engineer |
| **Repository** | `github.com/rpsg/oneops` @ `78adc1b` (branch `fix/webhook-nil-text-array-persistence`) |
| **Previous audit** | **None — this is the baseline.** Phase 6 trends are marked *Unknown* except where measured within this session |
| **Overall health** | **6.1 / 10** |

---

## 1. Executive Summary

The repository is **architecturally sound and operationally under-governed.** The code is clean by every static measure — zero lint issues, zero TODO markers in 14,498 production lines, a 0.90 test-to-code ratio, and 76.3% true coverage. What is weak is not the engineering; it is the **machinery that verifies the engineering**.

The defining measurement of this audit: **the repository has zero git remotes, so its six-job CI pipeline has never executed a single time.** Five of six release controls are specified but not operating. That single fact explains the GA defect (PM-001), the stale coverage metric, and the unchecked release checklist simultaneously.

Two measurements corrected long-standing beliefs. **True coverage is 76.3%, not the 61.9% CI reports** — CI omits the `integration` build tag while the largest package's tests all carry it. And **`internal/store/postgres` reports 1.3% in the default profile versus 73.7% in reality.**

## 2. Repository Snapshot

| Measure | Value |
|---|---|
| Commits (total) | **15** |
| Branches | **5** (`fix/…`, `m2-dependency-graph`, `m3-authority-resolver`, `master`, `release/v1.0.0`) |
| Merge commits | **0** · Pull requests: **0** · Distinct authors: **1** |
| Worktrees | 1 |
| **Dirty entries** | **22** (14 modified, 8 untracked) |
| Tags | `v0.1.0-m1`, `v0.2.0-m2`, `v1.0.0` — **`1.0.1` committed but not tagged** |
| Release cadence | 3 tags in **15h 07m** (07-22 08:44 → 23:51) |
| Production Go | **14,498 lines** |
| Test Go | **12,996 lines** · **448 test functions** · ratio **0.90** |
| Governance docs | rfc 2 · adr 2 · edr 1 · postmortem 1 · milestones 1 — **all uncommitted** |
| Open work packages | WP-1, WP-3, WP-4, WP-5, WP-6, WP-7 (M4); WP-2 complete but uncommitted |

## 3. Health Scorecard

| Area | Score | Evidence |
|---|---|---|
| **Architecture** | **8** | Arch rules 4/4 pass; `internal/domain` imports only `context`; consumer-declared ports throughout; single atomic mutation intact across three independent reviews. **−:** 8 of 12 §8 operations implemented; Guide §1.2 carried a measurably false rule until corrected this session |
| **Code Quality** | **8** | golangci-lint `./...` → **0 issues**; gofmt clean; **0 TODO/FIXME/XXX/HACK in 14,498 lines**; coherent error taxonomy (sentinel + typed + wrapped). **−:** a nil-value defect class shipped at three sites |
| **Testing** | **7** | 448 tests; ratio 0.90; **true coverage 76.3%**; integration green, **0 skips**; CI fails the build on a *skipped* integration test. **−:** no test exercised a zero value at the three defective sites; coverage metric misconfigured; **benchmarks unmeasured** (>10 min, twice) |
| **Operations** | **6** | `/healthz`, `/readyz`, `/metrics`; graceful shutdown; **rollback executed and proven** against a live DB; DR + 4 runbooks; audit-integrity scheduler with on-demand run. **−:** 4 declared compose services unused by any code; no operating CI |
| **Observability** | **5** | **40 metric definitions**, OTel tracing, structured `slog` with `request_id`. **−: 51 of 52 `mapError` sites discard the error cause** — proven to have concealed a defect for a full release. Metrics excellent, error observability near-absent |
| **Release Engineering** | **3** | **0 remotes → 6 CI jobs never executed**; checklist advisory not blocking (`[ ]` at GA, tagged anyway); **GA commit 152 files, tagged the same minute**; `v1.0.0` tag sits on a commit whose integration suite fails; VERSION/CHANGELOG stale until this session. **+:** atlas validate clean, docker builds, `make docker` injects version correctly, rollback proven |
| **Documentation** | **7** | Ratified Vols I–VI, Engineering Guide, 7 governance docs with recorded rationale; README/type comments accurate enough that the defect was *recognizable* as a defect. **−: the entire constitutional corpus is outside version control** while ADRs cite it; release checklist still carries unchecked boxes |
| **Repository Hygiene** | **4** | **22 dirty entries**; 5 branches, 0 merges; **3 tracked files under concurrent edit by another actor**; 3 independent changes interleaved in one tree; a pre-existing untracked file at root. **+:** no submodules, `go.mod`/`go.sum` clean, no generated-file drift |
| **Engineering Discipline** | **7** | Conventional commits; per-milestone tags; prior-milestone 0-diff discipline honored; ADR/EDR/postmortem practice established; measurement-before-implementation demonstrated repeatedly. **−:** 1 author, 0 PRs, 0 merges — **no independent review exists**; all discipline is self-enforced |
| **OVERALL** | **6.1** | Strong construction, weak verification machinery |

## 4. Trend Analysis

No prior audit exists. Trends below are measured **within this session only**.

| Area | Trend | Evidence |
|---|---|---|
| Testing | **Improving** | Integration suite went red → **green, 0 skips** — the first measured green run in repository history |
| Release Engineering | **Improving (from a very low base)** | `atlas migrate validate` verified for the first time; rollback executed for the first time; VERSION/CHANGELOG corrected |
| Documentation | **Improving** | 7 governance documents with rationale created; a false rule in Guide §1.2 corrected against measurement |
| **Repository Hygiene** | **Regressing** | Dirty entries grew **17 → 19 → 20 → 22** across the session |
| Architecture | **Stable** | No drift found in three independent reviews |
| Observability | **Stable** | Gap identified and design-reviewed; **not yet fixed** |
| Code Quality | **Stable** | Lint 0 issues throughout |

## 5. Risk Register

| # | Risk | Class | Evidence |
|---|---|---|---|
| R1 | **CI has never run.** Five of six controls are theoretical | **Critical** | `git remote -v` → 0; workflow requires `push`/`pull_request` to GitHub |
| R2 | **Coverage gate measures the wrong number.** DoD requires ≥80% on changed packages, evaluated against a profile understating by 14.4pp | **Critical** | 61.9% reported vs **76.3%** measured; `store/postgres` 1.3% vs **73.7%** |
| R3 | **Concurrent edits to tracked files by another actor.** `git commit -a` would sweep a third party's in-progress work into a release branch | **High** | `Makefile`, `docker-compose.yml`, `docker-compose.override.yml` modified externally |
| R4 | **Constitutional corpus has no version control.** ADRs cite documents with no history, diff, or recovery | **High** | Parent directory is not a git repository |
| R5 | Error causes unobservable in production | **High** | 51/52 `mapError` sites; concealed the GA defect for a full release |
| R6 | No independent review possible | **High** | 1 author, 0 merges, 0 PRs — organizational, not technical |
| R7 | `v1.0.0` tag on a commit whose integration suite fails | Medium | Reproduced in a clean clone |
| R8 | Benchmark regression status unknown | Medium | Suite exceeds 10 minutes, twice |
| R9 | 4 declared compose services unused by any Go code | Low | NATS, Redis, OpenSearch, MinIO — zero imports |
| R10 | 3 stale branches with no merge path | Low | `m2-dependency-graph`, `m3-authority-resolver`, `master` |

## 6. Top Five Improvements

Ranked by engineering value.

### 1. Configure a git remote so CI actually executes
- **Evidence:** 0 remotes; 6 jobs (build-test, integration, migrations, lint, security, docker) have never run.
- **Impact:** Activates five of six controls in one change. Every other improvement is unenforceable without it.
- **Effort:** Minutes. **Owner:** unassigned.
- **Verification:** An execution record showing **six green jobs on a real commit** — not "remote added."

### 2. Fix the coverage measurement
- **Evidence:** CI runs `go test ./... -coverprofile` without `-tags integration`; reports **61.9%** against a true **76.3%**.
- **Impact:** Makes the ≥80% Definition-of-Done gate meaningful. Currently it judges packages on a profile that excludes their tests.
- **Effort:** One line in `ci.yml`. **Owner:** unassigned.
- **Verification:** CI coverage summary prints ~76%, and `store/postgres` reports ~74% rather than 1.3%.

### 3. Make the release checklist blocking
- **Evidence:** The integration box was `[ ]` at `v1.0.0`; the tag was applied in the same minute as the commit.
- **Impact:** Converts an honest record into an actual gate; directly addresses the PM-001 escape path.
- **Effort:** Small. **Owner:** unassigned.
- **Verification:** Attempt a tag with an unchecked box and observe it **refused**.

### 4. Error-cause logging at the HTTP boundary
- **Evidence:** 51 of 52 sites discard the cause; the GA 500 emitted only `INFO http_request status:500`.
- **Impact:** Reduces mean-time-to-diagnose from *manual API probing* to *reading a log line*. Detection took 11h46m; the fix took 47m.
- **Effort:** ~65 lines, 52 mechanical and compile-enforced. Design reviewed → approved with changes. **Owner:** unassigned.
- **Verification:** Reintroduce the defect; observe an ERROR line naming SQLSTATE 23502 with matching `request_id`; assert 4xx paths emit **no** ERROR.

### 5. Land the three pending changes and clean the tree
- **Evidence:** 22 dirty entries; three independent changes interleaved; three files under concurrent external edit.
- **Impact:** Removes the risk of a mixed or third-party commit reaching a release branch; makes future `git status` a usable signal.
- **Effort:** Small — the changes touch disjoint paths. **Owner:** unassigned (external edits need their author).
- **Verification:** `git status --porcelain` returns only entries with an identified owner.

## 7. Overall Assessment

**6.1 / 10 — healthy construction, unhealthy verification.**

The spread across the scorecard is the finding. Architecture and Code Quality score 8; Release Engineering scores 3. This is not a repository accumulating sloppy code — by static measure it is cleaner than most production systems, with zero lint issues, zero TODO markers, and a near-1:1 test-to-code ratio. It is a repository whose **verification machinery was designed thoroughly and then never switched on.**

That distinction matters for what to do next. The instinct with a 6.1 is to write more tests or refactor. **The measurement says otherwise:** the tests already exist and are good — 448 of them, 76.3% real coverage, and an integration suite that catches the exact defect that shipped. They simply never ran. The highest-value work is not producing more engineering; it is **operating the engineering that already exists**, which is why improvements 1–3 are all governance rather than code.

The trend is genuinely positive where it has been touched: the integration suite is green for the first time, migration validation and rollback have been exercised rather than assumed, and a false rule in the Engineering Guide was corrected against measurement. The one regressing metric — repository hygiene, 17 → 22 dirty entries — is a direct consequence of doing that work without landing it, and improvement 5 closes it.

**Re-audit trigger:** after improvements 1–3, or at the next tag, whichever comes first.
