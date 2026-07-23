# RFC-001 — Automated Dependency-Rule Enforcement

| | |
|---|---|
| **Title** | Automated Dependency-Rule Enforcement |
| **Author** | Platform Engineering |
| **Status** | **Accepted** — APPROVE WITH CHANGES, see [RFC-001-change-review-record.md](RFC-001-change-review-record.md) |
| **Date** | 2026-07-23 |
| **Closes** | `EA-02` (Engineering Implementation Guide, Appendix A) |
| **Baseline** | `oneops/` @ branch `release/v1.0.0`, HEAD `9032021` (v1.0.0 GA) |
| **Decision record** | `docs/adr/ADR-ARCH-001-dependency-rule-enforcement.md` |
| **Reviewers** | Architecture owner (required — this change encodes a ratified law) |

> **Baseline correction (review finding D1).** This RFC originally cited `M3.3, commit 2331fb2` as its baseline. Measurement during review showed HEAD is `9032021` — three commits later, including M3.4, M3.5 and the v1.0.0 GA release. All import-graph measurements in this document were taken from the working tree, which **is** HEAD, so **the data is valid; only the label was wrong.** Corrected above.

---

## 1. Problem Statement

**The implementation problem.** The Engineering Implementation Guide §1.2, §2.2 and §2.4 state the repository's dependency rules — `internal/domain` imports nothing from the repository; `internal/httpapi` does not import `internal/store/*`; `sdk/` does not import `internal/`. Those rules are the Go realization of the ratified layer dependency law (Vol II Part 4). **Today they are enforced only by human code review.**

A rule enforced only by review has three known failure modes, all of which are cheap to hit and expensive to unwind:

1. A single import added under time pressure inverts a layer, and nothing fails.
2. The inversion is discovered months later, when the fix is a refactor rather than a one-line revert.
3. Every reviewer must hold the full rule set in their head on every PR, forever.

**Why now.** Two reasons, both concrete:

- The Implementation Guide (§10.4 code review, §10.5 architecture review) makes "does it respect the dependency direction?" a required review question. Making the question mechanical is what turns the guide from prose into a gate — and every later RFC's conformance claim rests on this one being automated.
- **The tree is green today** (measured — see §3.3). Landing the gate now is a *ratchet*: it costs nothing to adopt and permanently prevents regression. Every week it is deferred raises the odds it lands as a cleanup project instead.

**Milestone.** None. This is a cross-cutting engineering-quality change with no milestone dependency, which is precisely why it is a good first RFC: it exercises the RFC process at low blast radius. It does not block M3 and M3 does not block it.

---

## 2. Constitutional Alignment

This change **implements an existing law and adds nothing.** It creates no primitive, component, faculty, domain, or contract.

| Authority | What it fixes | How this implementation satisfies it |
|---|---|---|
| **Vol II Part 4** (Platform Layers) | *"A layer may depend only on the layers beneath it and on the Governance spine; it may never depend on a layer above it."* | The test encodes the monotonic rule for the package boundaries where it is mechanically decidable today. |
| **Vol II Part 4, L2 (Model)** | *"Forbidden dependencies: Perception, Cognition, Volition, Composition — the Model must not know how it is populated, reasoned over, acted upon, or displayed."* | Rule `domain-purity` forbids `internal/domain` from importing any repository package, in production **and** test files. |
| **Vol II §15.6** (Dependency rules) | `FORBIDDEN: depend on higher`, `FORBIDDEN: reach around a contract` | Rule `transport-not-persistence` prevents L7 transport reaching around a contract into L2 persistence internals. |
| **Vol II Part 11, Law 1** (Platform before Applications) | *"No application, deadline, or customer may compromise platform integrity."* | A deadline can no longer silently compromise the layer boundary; the build fails instead. |
| **Vol II Part 11, Law 8** (Stable Core, Evolvable Edge) | Core changes generationally; edge changes daily. | The rules protect the core (`domain`) strictly and leave the edge (`httpapi`, `sdk`, `web`) free within its boundary. |
| **Vol IV Principle 4** (Contracts, not internals) | *"No component reaches around a contract into another's internals."* | Enforced for the transport→persistence direction, which is the reach-around that actually occurs in practice. |
| **Vol I** (Constitution) | — | No Article engaged. This adds a build check, not a capability. |
| **Vol III** (Domain Model) | — | No semantic operation, primitive, relationship, or noun involved. |
| **Vol V** (Experience) | — | No human-facing surface. |
| **Vol VI** (AI Architecture) | — | See §7. |

**Conflicts found: none.** Every rule encoded is a restatement of a ratified constraint, narrowed to what is mechanically decidable.

> **One honest caveat, raised rather than resolved in code.** Implementation Guide §1.2 states a component "may never import a sibling component's internals." Measurement (§3.3) shows the codebase contains legitimate sibling edges — `governance → audit`, `compliance → timeline`, `ops → audit`, `ops → governance`, and `store/postgres → audit/events/governance/policy/timeline`. These are correct: an adapter must import the port's package to implement it, and `governance` legitimately composes the auditor inside its atomic mutation. **The guide's prose is overstated relative to both the code and the volumes** — Vol IV Principle 4 forbids reaching around a *contract*, not importing a sibling package at all. This RFC therefore **does not** encode a no-sibling-imports rule, and proposes a documentation correction to §1.2 (§12, AC-8). Encoding the guide's prose literally would have made the gate red on day one.

---

## 3. Existing Code

**Reuse first. This RFC adds no production code, no dependency, and no infrastructure.**

### 3.1 What already exists and is reused

| Asset | Reused how |
|---|---|
| `go list` (Go toolchain, already required) | The sole analysis mechanism. No parser, no `x/tools`, no new dependency. |
| `.github/workflows/ci.yml` → `build-test` job | The test runs inside the existing `go test ./... -race` step. **No new CI job.** |
| `Makefile` → `test` target | Runs locally with no new target required (an optional convenience target is proposed in §5.4). |
| `internal/config/config_production_test.go` | Precedent: a test that enforces a *policy* rather than a behavior. This follows the same pattern. |

### 3.2 What does not exist

- No architecture test, import-boundary test, or `internal/arch` package.
- No `docs/adr/` directory (tracked separately as `EA-20`; this RFC creates the first ADR and therefore the directory — see §11).
- No lint rule covering this. `.golangci.yml` enables `bodyclose`, `misspell`, `revive`, `gofmt`, `goimports` — none constrain import direction. `depguard` was considered and rejected (§4.6).

### 3.3 Measured current state

Full internal import graph extracted via `go list` at commit `2331fb2` (Go 1.26.5, local toolchain), for both default and `integration` build tags:

| Proposed rule | Production files | Test files | Verdict |
|---|---|---|---|
| `domain-purity` — `internal/domain` imports no repo package | **Clean** (imports only `context`) | **Clean** | ✅ passes |
| `transport-not-persistence` — `internal/httpapi` ↛ `internal/store/*` | **Clean** | **VIOLATES under `-tags integration`** | ✅ passes *(tests exempt — see below)* |
| `sdk-isolation` — `sdk/` ↛ `internal/` | **Clean** | **Clean** (tests import only `sdk`) | ✅ passes |
| `version-is-a-leaf` — `pkg/version` imports no repo package | **Clean** | **Clean** | ✅ passes |

**All four rules pass on the current tree.** This change is a ratchet, not a cleanup.

**The load-bearing detail:** under `-tags integration`, `internal/httpapi` test files legitimately import `internal/store/postgres` and `internal/store/migrate` — the integration tests wire a real Postgres repository behind the handler, which is exactly what §2.3 of the guide prescribes. A naive implementation that checked test files uniformly would fail on its first CI run and would be disabled within a week. `transport-not-persistence` therefore applies to production files only; the other three rules include test files, where measurement confirms they hold.

---

## 4. Design

### 4.1 Responsibilities

One new test package, `internal/arch`, with exactly one responsibility: **assert that the repository's import graph satisfies a declared rule set.** It contains no production code and is imported by nothing.

### 4.2 Interfaces

A declarative rule table — the entire extension surface. Adding a rule is one struct literal.

```go
// rule forbids every package under From from importing any package under a
// Forbidden prefix. Allow lists exact import paths exempted from the rule.
type rule struct {
    Name         string
    From         string   // package-path prefix the rule applies to
    Forbidden    []string // import-path prefixes that are forbidden
    IncludeTests bool     // also check TestImports / XTestImports
    Allow        []string // exact import paths exempted (with justification in a comment)
}
```

### 4.3 Full implementation

`internal/arch/deps_test.go` — copy verbatim; it is complete.

```go
// Package arch_test enforces the repository's dependency rules automatically.
//
// The rules encoded here are the Go realization of the ratified layer
// dependency law: a layer may depend only on the layers beneath it
// (Enterprise Architecture Vol II Part 4; dependency rules §15.6). They are
// documented for engineers in the Engineering Implementation Guide §1.2,
// §2.2 and §2.4. See docs/adr/ADR-ARCH-001.
//
// This package contains no production code. Adding a rule is one entry in
// `rules`; removing or exempting one requires an ADR update.
package arch_test

import (
    "encoding/json"
    "fmt"
    "os/exec"
    "path/filepath"
    "strings"
    "testing"
)

const module = "github.com/rpsg/oneops/"

type rule struct {
    Name         string
    From         string
    Forbidden    []string
    IncludeTests bool
    Allow        []string
}

// rules is the enforced dependency policy. Every entry cites the constraint it
// realizes. All four hold on the tree as of M3.3 (commit 2331fb2).
var rules = []rule{
    {
        // Vol II Part 4, L2: "the Model must not know how it is populated,
        // reasoned over, acted upon, or displayed."
        Name:         "domain-purity",
        From:         module + "internal/domain",
        Forbidden:    []string{module},
        IncludeTests: true,
    },
    {
        // Vol II §15.6: a higher layer must not reach around a contract into a
        // lower layer's internals. Transport delegates; it never persists.
        //
        // Tests are exempt: integration tests under `-tags integration`
        // legitimately wire a real repository behind the handler
        // (Implementation Guide §2.3).
        Name:         "transport-not-persistence",
        From:         module + "internal/httpapi",
        Forbidden:    []string{module + "internal/store/"},
        IncludeTests: false,
    },
    {
        // The published client is an external consumer. It composes the API,
        // never the platform's internals (Vol IV Part 10: no privilege).
        Name:         "sdk-isolation",
        From:         module + "sdk",
        Forbidden:    []string{module + "internal/"},
        IncludeTests: true,
    },
    {
        // Build metadata is a leaf: importable by anything, importing nothing.
        Name:         "version-is-a-leaf",
        From:         module + "pkg/version",
        Forbidden:    []string{module},
        IncludeTests: true,
    },
}

// pkg is the subset of `go list -json` output this test needs.
type pkg struct {
    ImportPath   string
    Imports      []string
    TestImports  []string
    XTestImports []string
}

func TestDependencyRules(t *testing.T) {
    // Both build configurations are checked: the integration surface is real
    // code and must obey the same rules.
    for _, tags := range [][]string{nil, {"integration"}} {
        pkgs := listPackages(t, tags)
        for _, r := range rules {
            for _, v := range violations(pkgs, r) {
                t.Errorf("dependency rule %q violated (tags=%v): %s", r.Name, tags, v)
            }
        }
    }
}

// violations returns one message per forbidden edge. It is pure so that the
// rule logic itself is testable without invoking the toolchain (TestViolations).
func violations(pkgs []pkg, r rule) []string {
    var out []string
    for _, p := range pkgs {
        if !under(p.ImportPath, r.From) {
            continue
        }
        imports := p.Imports
        if r.IncludeTests {
            imports = concat(p.Imports, p.TestImports, p.XTestImports)
        }
        for _, imp := range imports {
            // A package's own test binary imports the package itself; that is
            // never a violation.
            if imp == p.ImportPath || contains(r.Allow, imp) {
                continue
            }
            for _, f := range r.Forbidden {
                if strings.HasPrefix(imp, f) {
                    out = append(out, fmt.Sprintf("%s imports %s", p.ImportPath, imp))
                    break
                }
            }
        }
    }
    return out
}

// under reports whether path is prefix itself or a package beneath it.
func under(path, prefix string) bool {
    return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func contains(xs []string, x string) bool {
    for _, v := range xs {
        if v == x {
            return true
        }
    }
    return false
}

func concat(xss ...[]string) []string {
    var out []string
    for _, xs := range xss {
        out = append(out, xs...)
    }
    return out
}

// listPackages runs `go list -json ./...` at the module root and decodes the
// stream of package objects it emits.
func listPackages(t *testing.T, tags []string) []pkg {
    t.Helper()

    args := []string{"list", "-json"}
    if len(tags) > 0 {
        args = append(args, "-tags", strings.Join(tags, ","))
    }
    args = append(args, "./...")

    cmd := exec.Command("go", args...)
    cmd.Dir = moduleRoot(t)

    var stderr strings.Builder
    cmd.Stderr = &stderr

    out, err := cmd.Output()
    if err != nil {
        t.Fatalf("go list (tags=%v): %v\n%s", tags, err, stderr.String())
    }

    var pkgs []pkg
    dec := json.NewDecoder(strings.NewReader(string(out)))
    for dec.More() {
        var p pkg
        if err := dec.Decode(&p); err != nil {
            t.Fatalf("decode go list output: %v", err)
        }
        pkgs = append(pkgs, p)
    }
    if len(pkgs) == 0 {
        t.Fatal("go list returned no packages")
    }
    return pkgs
}

func moduleRoot(t *testing.T) string {
    t.Helper()
    out, err := exec.Command("go", "env", "GOMOD").Output()
    if err != nil {
        t.Fatalf("go env GOMOD: %v", err)
    }
    gomod := strings.TrimSpace(string(out))
    if gomod == "" || gomod == "/dev/null" {
        t.Fatal("not inside a Go module")
    }
    return filepath.Dir(gomod)
}
```

### 4.4 Lifecycle

Runs on every `go test ./...` — locally via `make test`, and in CI inside the existing `build-test` job. No scheduling, no state, no runtime presence. It ships in no binary: `_test.go` files are not compiled into `cmd/controlplane`.

### 4.5 Failure handling

- **A violated rule** → `t.Errorf` naming the rule, the build tags, the importing package, and the imported package. All violations are reported in one run, not just the first; a developer fixing three sees three.
- **Toolchain failure** (`go list` errors, e.g. a package that does not compile) → `t.Fatalf` with stderr attached. This is correct: an unanalysable tree is a failed check, never a silent pass. It will duplicate the compile error the build job already reports, which is acceptable noise.
- **Empty result** → `t.Fatal`. Guards against a silent pass if the working directory or module resolution is wrong.

### 4.6 Dependencies — and one rejected alternative

**No new dependency.** `encoding/json`, `os/exec`, `path/filepath`, `strings`, `testing` are stdlib; `go list` is already required to build.

**`depguard` (a `golangci-lint` linter) was considered and rejected.** It can express import restrictions in `.golangci.yml`, which is superficially tidier. It is rejected because:

1. It evaluates the configured build tags only — the measured `-tags integration` exemption in §3.3 is the entire subtlety of this change, and a config-file rule cannot express "forbidden in production files, permitted in integration tests" cleanly.
2. Lint failures are advisory in developer habit; a failing test is not.
3. It puts the constitutional rationale in a YAML file where the citation cannot live next to the rule.

### 4.7 State ownership

None. The test holds no state, reads no database, writes nothing.

### 4.8 Concurrency

None. Single-threaded. It shells out twice per run and is race-detector-safe by construction.

### 4.9 Security considerations

- Executes only `go list` and `go env`, with a fixed argument vector and no shell interpolation. No user input reaches the command line.
- Reads source metadata only; no network, no credentials, no filesystem writes.
- **Security-relevant in one direction:** it structurally protects the `sdk-isolation` boundary, preventing the published client from acquiring a transitive dependency on internal packages — which is how internal types leak into a public surface.

### 4.10 Performance

Two `go list` invocations, ~1–3s total on a warm module cache, dominated by the `-tags integration` pass. Measured against the existing `build-test` job (unit tests with `-race`, minutes), this is not material. Acceptance criterion AC-6 (§12) bounds it at 10s.

---

## 5. Public Contracts

### 5.1 API changes
**None.** No route, handler, DTO, or OpenAPI change. `internal/httpapi/openapi.yaml` is untouched and `sdk/` is not regenerated.

### 5.2 Events
**None.** No new operation, payload field, or subscriber-visible change.

### 5.3 Schemas
**None.** No migration (§6).

### 5.4 Configuration
**No runtime configuration, and no `Makefile` change.**

A `test-arch` convenience target was proposed here and **dropped in review (change C5)**: measurement confirmed `go test ./...` already runs the test both locally via `make test` and in the CI `build-test` job, so the target would have been surface with no function.

### 5.5 Feature flags
**None.** A gate that can be turned off is not a gate. Exempting a rule requires editing `rules` and updating ADR-ARCH-001 — a reviewed, attributable act.

### 5.6 Permissions
**None.** No RBAC permission, role, or `auth.Permission` constant.

---

## 6. Data Impact

| Concern | Impact |
|---|---|
| Database | **None.** No table, column, constraint, or query. |
| Indexes | **None.** |
| Caching | **None.** |
| Search | **None.** |
| Migration | **None.** No forward migration, therefore no rollback script and no `atlas.sum` change. |
| Backward compatibility | **Not applicable.** No persisted or wire-visible artifact is produced or consumed. N−1 compatibility is trivially preserved. |

---

## 7. AI Impact

**No AI impact.**

No reasoning, Context assembly, evidence, tool execution, guardrail, or model abstraction is touched. The change adds a build-time check over the import graph and has no runtime presence.

*(Forward-looking note, not a scope claim: when the AI path is built, `internal/arch` is the natural place to enforce Implementation Guide §9.1 — that no package named `ai`/`reasoning` exists and that a model-provider adapter imports no executor. That is a future RFC, not this one.)*

---

## 8. Testing

### 8.1 Unit — testing the test

The rule-checking logic is a pure function (`violations`) precisely so it can be tested without the toolchain. **A checker that is itself untested is a checker that silently passes.** Add `internal/arch/deps_logic_test.go`:

```go
func TestViolations(t *testing.T) {
    tests := []struct {
        name  string
        pkgs  []pkg
        rule  rule
        want  int
    }{
        {
            name: "clean domain produces no violation",
            pkgs: []pkg{{ImportPath: module + "internal/domain", Imports: []string{"context"}}},
            rule: rule{From: module + "internal/domain", Forbidden: []string{module}},
            want: 0,
        },
        {
            name: "domain importing a repo package is caught",
            pkgs: []pkg{{
                ImportPath: module + "internal/domain",
                Imports:    []string{module + "internal/audit"},
            }},
            rule: rule{From: module + "internal/domain", Forbidden: []string{module}},
            want: 1,
        },
        {
            name: "self-import via the external test package is not a violation",
            pkgs: []pkg{{
                ImportPath:   module + "sdk",
                XTestImports: []string{module + "sdk"},
            }},
            rule: rule{From: module + "sdk", Forbidden: []string{module + "internal/"}, IncludeTests: true},
            want: 0,
        },
        {
            name: "test import ignored when IncludeTests is false",
            pkgs: []pkg{{
                ImportPath:  module + "internal/httpapi",
                TestImports: []string{module + "internal/store/postgres"},
            }},
            rule: rule{From: module + "internal/httpapi", Forbidden: []string{module + "internal/store/"}},
            want: 0,
        },
        {
            name: "test import caught when IncludeTests is true",
            pkgs: []pkg{{
                ImportPath:  module + "internal/httpapi",
                TestImports: []string{module + "internal/store/postgres"},
            }},
            rule: rule{
                From: module + "internal/httpapi", Forbidden: []string{module + "internal/store/"},
                IncludeTests: true,
            },
            want: 1,
        },
        {
            name: "prefix match does not catch a sibling with a shared prefix",
            pkgs: []pkg{{
                ImportPath: module + "internal/domainx",
                Imports:    []string{module + "internal/audit"},
            }},
            rule: rule{From: module + "internal/domain", Forbidden: []string{module}},
            want: 0,
        },
        {
            name: "allow list exempts an exact path",
            pkgs: []pkg{{
                ImportPath: module + "internal/domain",
                Imports:    []string{module + "internal/audit"},
            }},
            rule: rule{
                From: module + "internal/domain", Forbidden: []string{module},
                Allow: []string{module + "internal/audit"},
            },
            want: 0,
        },
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            if got := len(violations(tt.pkgs, tt.rule)); got != tt.want {
                t.Errorf("violations() = %d, want %d", got, tt.want)
            }
        })
    }
}
```

The `internal/domainx` case is not hypothetical padding — it pins the `under()` boundary semantics, which a naive `strings.HasPrefix` would get wrong.

### 8.2 Integration
`TestDependencyRules` **is** the integration test: it runs the real toolchain over the real module in both build configurations.

### 8.3 Contract
Not applicable — no public contract changes (§5).

### 8.4 Performance
AC-6 (§12) bounds the test at 10s wall-clock. Measured expectation ~1–3s.

### 8.5 Security
No new attack surface (§4.9). No security test required. `gitleaks` and `trivy` are unaffected; no dependency is added, so no new CVE surface.

### 8.6 Regression
The strongest regression evidence available: **the full existing suite must remain green and unchanged.** No production file is modified, so any test change would itself be a defect. Verify M3.1/M3.2/M3.3 authority tests remain byte-identical and passing, consistent with the frozen-baseline discipline applied at each M3 story.

---

## 9. Rollback

**Trivial and total.** The change is additive, test-only, and touches no production code, schema, or contract.

| Step | Action |
|---|---|
| Full revert | `git revert <commit>` — removes `internal/arch/` and the optional Makefile target. Nothing else is affected. |
| Partial revert (a single rule is wrong) | Delete or `Allow`-list the offending entry in `rules`, and record why in ADR-ARCH-001. One-line change, reviewable. |
| Emergency unblock (a rule is blocking an urgent fix) | Add the specific import to that rule's `Allow` list with a justifying comment, land the urgent fix, then remove the exemption in a follow-up. **The exemption must be an explicit, attributable line of code** — this is deliberately more visible than a config toggle. |

**No rollback preconditions apply:** no migration, no N−1 schema concern, no deployed artifact, no feature flag, no data to restore. The deployed binary is byte-identical with and without this change.

---

## 10. Risks

| # | Risk | Severity | Assessment & mitigation |
|---|---|---|---|
| R1 | **False positive blocks legitimate work**, and the team disables the gate. | **High** — this is how architecture tests usually die. | The measured `-tags integration` exemption (§3.3) removes the one known false positive before it occurs. Rule set is deliberately minimal — four rules, all verified green. `Allow` provides an attributable escape hatch (§9) so the gate is never the only path forward. |
| R2 | **Rules drift from the Implementation Guide**, and the two disagree. | Medium | Already true today and this RFC surfaces it: guide §1.2's no-sibling-imports prose is overstated (§2 caveat). AC-8 corrects the guide in the same PR. Ongoing: each rule cites its authority inline, so a reviewer can check the rule against the volume without leaving the file. |
| R3 | **Toolchain dependency** — the test shells out to `go`. | Low | The toolchain is already required to build and test; CI provides it via `actions/setup-go`. Failure is loud (`t.Fatalf` with stderr), never a silent pass. |
| R4 | **False sense of coverage** — "architecture is enforced" when only four narrow rules are. | Medium | Stated explicitly here and in ADR-ARCH-001: this enforces *import direction*, not layering semantics. It cannot detect a domain type shaped by an API response, a projection treated as truth, or a widened constitutional transaction. Guide §10.5 architecture review remains required for those, and R4 is the reason it does. |
| R5 | **CI time increase.** | Low | ~1–3s inside a job measured in minutes. AC-6 bounds it. |
| R6 | **Operational risk.** | **None** | No runtime presence. The shipped binary is unchanged. |
| R7 | **Security risk.** | **None** | Fixed argument vector, no shell, no network, no writes (§4.9). Net-positive via `sdk-isolation`. |
| R8 | **Performance risk (runtime).** | **None** | Test-only; not compiled into `cmd/controlplane`. |

---

## 11. ADR Check

**Verdict: create a new ADR — `ADR-ARCH-001: Dependency rules are enforced by an automated test`.**

**Justification.** Implementation Guide Appendix B requires an ADR when adopting or changing any `[EA-nn]`. This change adopts **EA-02** and makes it permanent, so an ADR is mandatory, not discretionary. Three decisions in this RFC need a durable record with rationale, because each will otherwise be re-litigated by whoever next hits the gate:

1. **A test, not a linter** (`depguard` rejected — §4.6).
2. **Test files are exempt from `transport-not-persistence`**, and why (integration tests legitimately wire a real store — §3.3). Without the recorded rationale this reads as an oversight and someone will "fix" it.
3. **No no-sibling-imports rule**, and why (Vol IV Principle 4 forbids reaching around a contract, not sibling imports — §2 caveat).

**This creates `oneops/docs/adr/`**, closing the structural half of `EA-20`. Back-filling `ADR-AUDIT-003/004/005` from existing code comments is explicitly **out of scope for this RFC** and remains open under EA-20 — this RFC establishes the directory and format; it does not discharge the back-fill.

**Existing ADRs to update:** none. `ADR-AUDIT-003/004/005` are untouched.

**New ADR candidates recorded, not resolved here:**
- **ADR candidate:** whether the layer→package mapping in Implementation Guide §2.2 (`EA-03`) should itself become enforceable once L3–L6 are implemented. Not decidable today — those layers do not exist yet.

---

## 12. Acceptance Criteria

Objective and individually checkable. This RFC is implemented when all pass.

| # | Criterion | Verification |
|---|---|---|
| **AC-1** | `internal/arch/deps_test.go` exists, encoding the four rules of §4.3, each with an inline citation of the constraint it realizes. | Code review |
| **AC-2** | `internal/arch/deps_logic_test.go` exists with the seven table cases of §8.1, all passing. | `go test ./internal/arch/...` |
| **AC-3** | `go test ./... -race` is **green on the current tree** with no production-code change. | CI `build-test` job |
| **AC-4** | The gate **actually fails** when violated, naming the rule and the offending edge. | **Pre-verified** — see Appendix §A.2. Re-confirm in the PR. |
| **AC-5** | Both build configurations are checked — the failure message distinguishes `tags=[]` from `tags=[integration]`. | Code review + AC-4 output |
| **AC-6** | `go test ./internal/arch/...` completes in **< 10s** on a warm module cache. | **Pre-verified at 1.59s** — Appendix §A.2 |
| **AC-7** | `ADR-ARCH-001` exists at `oneops/docs/adr/ADR-ARCH-001-dependency-rule-enforcement.md`, in the Implementation Guide Appendix B format, recording the three decisions of §11. | Code review |
| **AC-8** | Implementation Guide **§1.2 is corrected**: the no-sibling-imports claim is replaced with the accurate rule (a component must not reach around a published contract), and **EA-02 is marked closed** in Appendix A with a pointer to ADR-ARCH-001. | Code review |
| **AC-9** | `go vet ./...`, `golangci-lint run ./...`, and `gofmt -l` are clean on the new files. | CI `lint` job |
| **AC-10** | No change to: `go.mod`, `go.sum`, `openapi.yaml`, `sdk/`, any migration, `atlas.sum`, or any file under `internal/` other than the new `internal/arch/` directory. | `git diff --stat` in the PR |

**Definition of Done** additionally applies in full (Implementation Guide §10.1), except the items inapplicable to a test-only change: no OpenAPI/SDK regeneration (§5.1), no migration or rollback script (§6), no new configuration or production-guard case (§5.4).

---

## Appendix — Measurement Record

### A.1 Import graph

Extracted at commit `2331fb2` (branch `m3-authority-resolver`), Go 1.26.5 darwin/arm64, local toolchain, via `go list`. Reproduce with:

```bash
go list -f '{{.ImportPath}}|{{join .Imports ","}}' ./... | grep rpsg/oneops
```

**Non-test internal edges — 20 packages, 46 edges.** Legitimate sibling and adapter edges observed (each consistent with Vol IV Principle 4, none reaching around a contract):

- `governance → audit` — the atomic mutation composes the auditor (`ADR-AUDIT-005`)
- `compliance → timeline` — compliance composes the timeline read model
- `ops → audit`, `ops → governance` — operational instrumentation over both
- `store/postgres → audit, events, governance, policy, timeline` — the adapter implements ports declared by each consumer
- `cmd/controlplane → 14 packages` — the composition root, explicitly permitted (Implementation Guide §1.2)

**No cycles** (Go rejects them at compile time; noted for completeness).

**The four proposed rules held in every configuration measured.**

### A.2 Pre-verification of the proposed implementation

The code in §4.3 and the tests in §8.1 were **written and executed against the real module** before this RFC was submitted, then removed to leave the tree unmodified. This is evidence for the review, not a claim of completion — the implementation PR still has to land the files.

**Positive path** — `go test ./internal/arch/... -v`:

```
--- PASS: TestViolations (0.00s)
    --- PASS: TestViolations/clean_domain_produces_no_violation
    --- PASS: TestViolations/domain_importing_a_repo_package_is_caught
    --- PASS: TestViolations/self-import_via_the_external_test_package_is_not_a_violation
    --- PASS: TestViolations/test_import_ignored_when_IncludeTests_is_false
    --- PASS: TestViolations/test_import_caught_when_IncludeTests_is_true
    --- PASS: TestViolations/prefix_match_does_not_catch_a_sibling_with_a_shared_prefix
    --- PASS: TestViolations/allow_list_exempts_an_exact_path
--- PASS: TestDependencyRules (0.48s)
ok  github.com/rpsg/oneops/internal/arch  1.592s
```

`gofmt -l internal/arch` clean · `go vet ./internal/arch/...` clean · **1.592s** against the 10s budget (AC-6) · `TestDependencyRules` covers both tag sets in 0.48s (AC-5).

**Negative path (AC-4).** Rather than temporarily corrupting a production file, the probe added a deliberately-violated rule — `internal/httpapi` must not import `internal/audit`, an edge that genuinely exists — proving the gate detects a real violation end-to-end:

```
--- FAIL: TestProbeDetectsRealViolation (0.22s)
    probe_test.go:11: dependency rule "probe" violated (tags=[]):
        github.com/rpsg/oneops/internal/httpapi imports github.com/rpsg/oneops/internal/audit
FAIL
```

The message names the rule, the build tags, the importing package and the imported package — the diagnostic quality required by §4.5.

**Tree restored.** `internal/arch/` was removed after verification; `git status` shows no modification to any tracked file. The only addition this RFC makes to the working tree is `docs/rfc/`.
