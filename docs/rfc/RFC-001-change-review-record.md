# Engineering Change Review — RFC-001

## Dependency-Rule Enforcement · Measure → Verify → RFC → Implement

| | |
|---|---|
| **Change under review** | Automated dependency-rule enforcement (RFC-001) |
| **Reviewer role** | Architecture Reviewer / Repository Maintainer |
| **Date** | 2026-07-23 |
| **Repository state at review** | branch `release/v1.0.0`, HEAD `9032021`, tag `v1.0.0` present |
| **Toolchain** | Go 1.26.5 (local) · golangci-lint 2.12.2 (Docker) · Docker 28.4.0 |
| **Recommendation** | **APPROVE WITH CHANGES** (§7) |

Every statement below traces to a measurement command or a ratified constitutional decision. No section contains speculation.

---

# 1. Repository Measurements

## 1.1 Toolchain and environment

| Tool | Measured | Consequence for this change |
|---|---|---|
| `go` | **1.26.5** darwin/arm64, local, builds the go1.25 module | All build/test/vet verification runs locally |
| `golangci-lint` | **NOT installed locally**; **2.12.2** (built go1.26.2) via Docker | Lint verified via Docker; see §1.5 finding F5 |
| `atlas` | **NOT installed locally** | Not required — this change adds no migration |
| `docker` | **server 28.4.0, healthy** | Contradicts recorded program state; see D3 |

## 1.2 Repository state

```
branch:  release/v1.0.0
HEAD:    9032021  Release v1.0.0 GA — platform baseline + release hardening
         15b3c5f  M3.5: Operational Gap Resolution (noGapIfRemoved clause)
         21eeb75  M3.4: Active Citation Resolution (noActiveArtifactCites clause)
         2331fb2  M3.3: Responsibility Ownership Resolution
tags:    v0.1.0-m1, v0.2.0-m2, v1.0.0
working tree: clean except untracked docs/rfc/ and ONEOPS_VERSION_1_0_GA_RATIFICATION.md
```

## 1.3 Existing implementation, conventions, governance

| Measured | Result |
|---|---|
| Packages | **20** (`go list ./...`), one module `github.com/rpsg/oneops` |
| Internal import edges (non-test) | **46** |
| Existing architecture/import test | **None.** No `internal/arch`, no import-boundary test anywhere |
| Existing lint coverage of imports | **None.** `.golangci.yml` (v2) enables `bodyclose`, `misspell`, `revive`, `gofmt`, `goimports` — none constrain import direction |
| Existing ADRs | **None.** `docs/adr/` **does not exist**. `ADR-AUDIT-003/004/005` are referenced only in code comments, CHANGELOG, and runbooks |
| Existing RFCs | **One** — `docs/rfc/RFC-001-dependency-rule-enforcement.md` (the change under review) |
| Baseline coverage | **61.9%** total statements (`go tool cover -func`) |
| Precedent for policy-as-test | **Yes** — `internal/config/config_production_test.go` enforces configuration policy rather than behavior |

## 1.4 Rule conformance at HEAD

Measured via `go list -json ./...` under default **and** `-tags integration`:

| Rule | Production files | Test files | Verdict |
|---|---|---|---|
| `domain-purity` — `internal/domain` imports no repo package | Clean (`context` only) | Clean | ✅ |
| `transport-not-persistence` — `internal/httpapi` ↛ `internal/store/*` | Clean | **Violates under `-tags integration`** | ✅ *(tests exempt)* |
| `sdk-isolation` — `sdk/` ↛ `internal/` | Clean | Clean | ✅ |
| `version-is-a-leaf` — `pkg/version` imports no repo package | Clean | Clean | ✅ |

**All four rules hold at HEAD.** The change is a ratchet, not a remediation.

Legitimate edges measured and deliberately **not** forbidden: `governance→audit`, `compliance→timeline`, `ops→audit`, `ops→governance`, `store/postgres→{audit,events,governance,policy,timeline}`, `cmd/controlplane→14 packages`.

## 1.5 Discrepancies — measurement vs documentation

Per protocol: **measurement wins; the discrepancy is recorded.**

| # | Documentation claims | Measurement shows | Disposition |
|---|---|---|---|
| **D1** | RFC-001 header: baseline `M3.3, commit 2331fb2` | HEAD is `9032021` (v1.0.0 GA) — **3 commits later**, including a GA release | RFC-001 header is **stale**. The import-graph data was taken from the working tree (= HEAD), so **the data is valid and the label is wrong**. Correct the header. |
| **D2** | Program state: `noActiveArtifactCites` + `noGapIfRemoved` "STILL DEFERRED → M5 Conformance / M4 Baseline" | `internal/authority/citation.go` (M3.4) and `gap.go` (M3.5) are **implemented and committed** | **All four Replacement-Test conjuncts are now implemented.** Recorded program state is stale. No impact on this change. |
| **D3** | Program state: Docker containerd I/O-corrupted; lint / integration-run / docker-build "NOT TESTED" | Docker server **28.4.0 healthy**; golangci-lint executed successfully | Recorded blocker is **resolved**. This removes the standing verification gap carried since M3.1. |
| **D4** | Engineering Implementation Guide §1.2: a component "may never import a sibling component's internals" | 8 legitimate sibling/adapter edges exist at HEAD (§1.4) | Guide prose is **overstated** vs both the code and Vol IV Principle 4, which forbids reaching around a *contract*. Correct the Guide. |
| **F5** | *(new observation, not a defect)* | golangci-lint **v2.1.6 cannot load `.golangci.yml`** — built with go1.24, module targets go1.25. **v2.12.2 works, 0 issues** | CI uses `version: latest`, so CI is unaffected. Pinning golangci-lint below the module's Go version would break the `lint` job. Worth a note in CONTRIBUTING. |

---

# 2. Constitutional References

| Reference | Requirement | Current implementation | Compliance | Conflict |
|---|---|---|---|---|
| **Vol II Part 4** (Platform Layers) | A layer may depend only on layers beneath it and the Governance spine | Holds at HEAD (§1.4); enforced by human review only | ✅ Compliant, **unenforced** | None |
| **Vol II Part 4, L2** (Model) | The Model must not know how it is populated, reasoned over, acted upon, or displayed | `internal/domain` imports only `context` | ✅ Compliant | None |
| **Vol II §15.6** (Dependency rules) | Forbidden: depend on higher; reach around a contract | `httpapi` does not import `store/*` in production files | ✅ Compliant | None |
| **Vol II Part 11, Law 1** (Platform before Applications) | No deadline may compromise platform integrity | No mechanism prevents a deadline-driven inversion | ⚠️ **Compliant but unprotected** | None — this is the gap the change closes |
| **Vol II Part 11, Law 8** (Stable Core, Evolvable Edge) | Core generational, edge daily | Rules bind `domain` strictly, leave `httpapi`/`sdk` free within boundary | ✅ Compliant | None |
| **Vol IV Principle 4** (Contracts, not internals) | No component reaches around a contract into another's internals | Measured sibling edges are contract-satisfying, not reach-arounds (§1.4) | ✅ Compliant | **Guide §1.2 conflicts with this** — see D4 |
| **Vol I, Vol III, Vol V, Vol VI** | — | No Article, primitive, operation, human surface, or reasoning path engaged | ✅ Not applicable | None |

**Constitutional conflicts found: none.** One *documentation* conflict (D4), corrected by this review rather than by code.

---

# 3. Gap Analysis

Against the protocol's A–E taxonomy:

| Outcome | Applies? | Evidence |
|---|---|---|
| **A. Already implemented** | **No** | Measured: no architecture test, no import lint rule, no ADR (§1.3) |
| **B. Implementation incomplete** | **YES** | The ratified dependency law (Vol II Part 4) is satisfied at HEAD but **enforced only by human review**. Nothing prevents regression |
| **C. Documentation incorrect** | **YES** | Four discrepancies measured — D1 (RFC baseline), D2 (program state), D3 (Docker), D4 (Guide §1.2) |
| **D. Architecture assumption missing** | **YES** | `EA-02` is an open engineering assumption with no ADR; `docs/adr/` does not exist |
| **E. Real engineering defect** | **No** | All four rules pass at HEAD. There is nothing to fix, only something to protect |

**Conclusion: B + C + D.** Work is justified. Notably **not E** — this is preventive, and the change must be scoped accordingly (no remediation, no refactor).

---

# 4. Proposed Change

Minimal implementation. Nothing beyond what §3 justifies.

### 4.1 Files

| File | Status | Purpose |
|---|---|---|
| `internal/arch/deps_test.go` | **New** | Four rules + `go list` analysis + assertion |
| `internal/arch/deps_logic_test.go` | **New** | 7-case table test of the pure `violations()` function |
| `docs/adr/ADR-ARCH-001-dependency-rule-enforcement.md` | **New** | Records the three decisions (§6) — creates `docs/adr/` |
| `docs/rfc/RFC-001-dependency-rule-enforcement.md` | **Amend** | Correct stale baseline (D1); mark Accepted |
| `OneOps-Engineering-Implementation-Guide-v1.0.md` | **Amend** | Correct §1.2 (D4); close EA-02 in Appendix A |

### 4.2 Packages

One new package, `internal/arch` — **test-only, zero production statements.** Measured: reports `coverage: [no statements]`; **total coverage unchanged at 61.9%** (no dilution). Not compiled into `cmd/controlplane`.

### 4.3 Interfaces

One unexported struct, `rule`, and one pure function, `violations([]pkg, rule) []string`. No exported API. No interface added to any production package.

### 4.4 Tests

Both files are tests. `violations()` is pure specifically so the checker is itself tested (`TestViolations`) — a checker that is untested is a checker that silently passes.

### 4.5 Migration

**None.** No schema, no `atlas.sum` change, no rollback script, no data.

### 4.6 Rollback

`git revert`. Test-only and additive; the deployed binary is byte-identical with and without it. Partial rollback = delete or `Allow`-list one rule entry.

### 4.7 Risk

| Risk | Severity | Measured mitigation |
|---|---|---|
| False positive disables the gate | High → **Mitigated** | The one real false positive (integration tests importing the store) was measured before implementation and exempted |
| Coverage dilution breaks the CI summary | Medium → **Eliminated** | Measured: 61.9% → 61.9%, `go tool cover -func` unaffected |
| Build/vet breakage from a test-only package | Medium → **Eliminated** | Measured: `go build ./...` OK, `go vet ./...` OK |
| Lint failure on new files | Medium → **Eliminated** | Measured: golangci-lint 2.12.2 → **0 issues** |
| CI time increase | Low | Measured **2.28s** under `-race`, inside a job measured in minutes |
| False sense of coverage | Medium | Enforces *import direction* only. Cannot detect a domain type shaped by an API response, a projection treated as truth, or a widened constitutional transaction. Recorded in ADR-ARCH-001 |
| Runtime / security / performance | **None** | No runtime presence; fixed argv, no shell, no network, no writes |

### 4.8 Explicitly descoped

- **The optional `make test-arch` target** proposed in RFC-001 §5.4 — **dropped.** Measurement shows `go test ./...` already runs it locally and in CI. It is surface with no function.
- **Back-filling `ADR-AUDIT-003/004/005`** — remains open under EA-20. This change creates the directory and format; it does not discharge the back-fill.
- **Any rule for layers L3–L6** — those layers are not implemented; there is nothing to measure and therefore nothing to enforce.

---

# 5. Verification Plan

Every claim testable; all executed at HEAD `9032021`.

| # | Verification | Command | Result |
|---|---|---|---|
| V1 | Unit — rule logic | `go test ./internal/arch/... -run TestViolations` | **PASS**, 7/7 cases |
| V2 | Integration — real module, both tag sets | `go test ./internal/arch/... -run TestDependencyRules` | **PASS**, 0.48s |
| V3 | **Negative — gate detects a real violation** | probe rule `httpapi ↛ audit` (an edge that genuinely exists) | **FAIL as required**, message names rule, tags, and both packages |
| V4 | Build integrity | `go build ./...` | **OK** |
| V5 | Vet | `go vet ./...` | **OK** |
| V6 | Format | `gofmt -l internal/arch` | **clean** |
| V7 | Lint | golangci-lint 2.12.2 `run ./internal/arch/...` | **0 issues** |
| V8 | Regression — full suite | `go test ./... -race` | **all green**, no failures |
| V9 | Coverage non-dilution | `go tool cover -func` before/after | **61.9% → 61.9%** |
| V10 | Performance | `go test ./internal/arch/... -race` | **2.28s** (budget 10s) |
| V11 | Repository validation | `git status --short` | No tracked file modified |

**Not verified, and why:** the `integration` CI job (`-tags integration` against live Postgres) was not executed — this change adds no SQL and the integration job's package selectors (`./internal/store/postgres/...`, `./internal/httpapi/...`) exclude `internal/arch`. `atlas migrate validate` not run — no migration added.

---

# 6. Engineering Impact

| Artifact | Required? | Justification |
|---|---|---|
| **ADR** | **YES — `ADR-ARCH-001`** | Guide Appendix B mandates an ADR when adopting an `[EA-nn]`; this adopts EA-02 permanently. Three decisions need a durable record or they will be re-litigated: (1) a test rather than `depguard`; (2) test files exempt from `transport-not-persistence`, and why; (3) deliberately **no** no-sibling-imports rule (D4) |
| **Engineering Guide update** | **YES** | §1.2 correction (D4) and EA-02 closure in Appendix A. Without it the Guide and the enforced rules disagree |
| **RFC update** | **YES** | RFC-001 header carries a stale baseline (D1); status moves Proposed → Accepted |
| **Architecture review** | **YES — this document** | The change encodes a ratified law (Vol II Part 4). Guide §10.5 requires review when a change touches a layering rule |
| **Program-state correction** | **Recommended, out of scope here** | D2 and D3 are stale program records, not engineering artifacts. Flagged for the record owner |

---

# 7. Acceptance Decision

## **APPROVE WITH CHANGES**

**Approved because**, on measured evidence:

- The gap is real and is category **B + C + D**, not speculation — the ratified dependency law is unenforced (§3).
- The change is **minimal**: two test files, zero production statements, zero dependencies, zero runtime presence (§4).
- It is a **ratchet, not a remediation** — all four rules pass at HEAD (§1.4), so it can land green and cannot become a cleanup project.
- Every risk that would normally kill an architecture test was **measured and eliminated before proposal**, not argued away: the integration-test false positive (§1.4), coverage dilution (V9), build/vet/lint breakage (V4/V5/V7).
- The failure path is **proven**, not assumed (V3).

**Required changes before merge:**

| # | Change | Source |
|---|---|---|
| **C1** | Correct RFC-001's baseline header: `release/v1.0.0` @ `9032021`, not `M3.3 @ 2331fb2`. Measurements were taken at HEAD and remain valid | D1 |
| **C2** | Correct Guide §1.2 — replace the no-sibling-imports claim with the accurate rule (a component must not reach around a published contract), citing Vol IV Principle 4 | D4 |
| **C3** | Close `EA-02` in Guide Appendix A with a pointer to ADR-ARCH-001 | §6 |
| **C4** | Create `ADR-ARCH-001` recording the three decisions | §6 |
| **C5** | Drop the `make test-arch` target from RFC-001 §5.4 — measured unnecessary | §4.8 |
| **C6** | Do **not** commit to `release/v1.0.0`. Branch first | §1.2 |

**Advisory, not blocking:**

- **A1** — Record in CONTRIBUTING that golangci-lint must be built with Go ≥ the module's target; v2.1.6 fails to load the config, v2.12.2 works (F5).
- **A2** — Program state carries two stale records (D2: M3.4/M3.5 are implemented, not deferred; D3: Docker is healthy, the standing verification blocker is resolved). These are record-keeping corrections for the owner of that record, not engineering work.

**Not approved / out of scope:** ADR-AUDIT back-fill (EA-20), any rule for unimplemented layers, any Makefile surface.

---

## Appendix — Reproduction

```bash
export PATH=/opt/homebrew/bin:$PATH GOTOOLCHAIN=local

# Rule conformance at HEAD
go list -f '{{.ImportPath}}|{{join .Imports ","}}' ./... | grep rpsg/oneops

# Full verification
go build ./... && go vet ./... && gofmt -l internal/arch
go test ./... -race
go test ./... -coverprofile=cov.out -covermode=atomic && go tool cover -func=cov.out | tail -1

# Lint (local golangci-lint is not installed; must be built with Go >= 1.25)
docker run --rm -v "$PWD":/src -w /src golangci/golangci-lint:latest golangci-lint run ./internal/arch/...
```
