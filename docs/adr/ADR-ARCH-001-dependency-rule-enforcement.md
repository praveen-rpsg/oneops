# ADR-ARCH-001: Dependency rules are enforced by an automated test

- **Status:** Accepted
- **Date:** 2026-07-23
- **Constitutional authority:** Enterprise Architecture Vol II Part 4 (Platform Layers — the monotonic dependency rule); Vol II §15.6 (allowed vs forbidden dependencies); Vol IV Principle 4 (contracts, not internals)
- **Laws engaged:** Vol II Part 11 — Law 1 (Platform before Applications), Law 8 (Stable Core, Evolvable Edge)
- **Supersedes / relates to:** closes `EA-02` (Engineering Implementation Guide, Appendix A); first ADR in this repository, therefore establishes `docs/adr/` and the format
- **Evidence:** `docs/rfc/RFC-001-dependency-rule-enforcement.md`, `docs/rfc/RFC-001-change-review-record.md`

## Context

The ratified layer dependency law (Vol II Part 4) fixes what may depend on what. Measurement at `release/v1.0.0` @ `9032021` confirms the repository satisfies it: `internal/domain` imports only `context`; `internal/httpapi` does not import `internal/store/*` in production files; `sdk/` imports nothing from `internal/`; `pkg/version` imports nothing from the repository.

The law was enforced only by human code review. Nothing prevented a single import from inverting a layer, and nothing would have failed if one did. The repository had no architecture test, and `.golangci.yml` enables no linter that constrains import direction.

This is a gap in **enforcement**, not in compliance — the tree was already clean. That distinction determines the scope: preventive, not remedial.

## Decision

Enforce the rules with a test-only package, `internal/arch`, that reads the import graph via `go list -json ./...` under both the default and `integration` build tags, and fails the build on a forbidden edge. Four rules are enforced: `domain-purity`, `transport-not-persistence`, `sdk-isolation`, `version-is-a-leaf`.

Three sub-decisions need recording, because each will otherwise be re-litigated by whoever next hits the gate.

### 1. A test, not a linter

`depguard` (available in golangci-lint) can express import restrictions in `.golangci.yml`. It is rejected because it evaluates one build-tag configuration, and the production-vs-integration-test distinction below is the entire subtlety of this change; because lint findings are treated as advisory in practice where a failing test is not; and because a YAML rule cannot carry its constitutional citation next to the rule.

### 2. Test files are exempt from `transport-not-persistence`

Under `-tags integration`, `internal/httpapi` test files import `internal/store/postgres` and `internal/store/migrate`. This is **correct** — the integration tests wire a real repository behind the handler, exactly as the Implementation Guide §2.3 prescribes.

A rule that checked test files uniformly would have failed on its first CI run and been disabled within a week. The exemption is deliberate and measured, not an oversight. The other three rules **do** include test files, where measurement confirms they hold.

### 3. There is deliberately no "no sibling imports" rule

Implementation Guide §1.2 stated that a component "may never import a sibling component's internals." Measurement contradicts this. Eight legitimate sibling and adapter edges exist: `governance→audit`, `compliance→timeline`, `ops→audit`, `ops→governance`, and `store/postgres→{audit,events,governance,policy,timeline}`.

These are correct. An adapter must import a port's package to implement it, and `governance` legitimately composes the auditor inside its atomic mutation (`ADR-AUDIT-005`). Vol IV Principle 4 forbids reaching around a **contract** — not importing a sibling package.

The Guide's prose was overstated relative to both the code and the volumes, and has been corrected. Encoding it literally would have made the gate red on day one.

## Consequences

**Easier.** Layer inversion becomes impossible to merge silently. The Guide §10.4/§10.5 review question "does it respect the dependency direction?" becomes mechanical, so reviewers spend attention on the questions that cannot be automated. Adding a rule is one struct literal.

**Harder.** A genuine need to cross a boundary now requires an explicit, attributable `Allow` entry with a justifying comment and an update to this ADR — deliberately more visible than a config toggle.

**What this does not do.** It enforces **import direction only**. It cannot detect a domain type shaped by an API response, a projection treated as truth, a component holding private truth, or a widened constitutional transaction. Guide §10.5 architecture review remains required for those, and this limitation is the reason.

**Measured cost.** 2.28s under `-race`; total coverage unchanged (61.9% → 61.9%, the package reports `coverage: [no statements]`); `go build ./...`, `go vet ./...`, `gofmt`, and golangci-lint 2.12.2 all clean; no dependency added; not compiled into `cmd/controlplane`.

## Reversal

`git revert` the commit. The change is additive and test-only; the deployed binary is byte-identical with and without it, so reversal costs nothing operationally and requires no migration, flag, or coordination.

To retire a single rule rather than the mechanism, delete its entry from `rules` and record why here. To exempt one import without retiring the rule, add the exact path to that rule's `Allow` list with a justifying comment.
