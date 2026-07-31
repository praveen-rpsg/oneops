# ADR-PKG-001 — The knowledge graph is generated, never committed

| | |
|---|---|
| **Status** | **ACCEPTED** |
| **Resolves** | EAR blocker B3 |
| **Related** | PKG Design §4/§7, PKG Implementation Specification §VII, §IX |

## Question

Is `pkg.json` a committed repository artifact, generated-only, or committed on
release branches?

## Repository evidence

`atlas.sum` is the only committed generated artifact in the tree. The Phase 3
contract already recorded it as a conflict source: *any two migration-adding
streams conflict on it.* `pkg.json` is strictly worse — it is derived from the
whole tree, so **every** change regenerates it, and every concurrent pull
request would conflict.

The Engineering Authorization Review recorded the consequence: with `pkg.json`
committed, a CI failure cannot be attributed to a single engineering change.

## Decision

**`pkg.json` is generated only. It is never committed.** CI regenerates it on
every run and publishes it as a build artifact. `.gitignore` excludes it.

## Alternatives considered

- **Committed (design default).** Rejected: imposes a merge-conflict tax on
  every pull request, and requires a freshness test whose only purpose is to
  detect staleness the commitment itself created.
- **Committed on release branches only.** Rejected: staleness returns between
  releases, and the artifact is least useful exactly when it is most trusted.

## Consequences

**The freshness problem does not exist.** A file that is never committed cannot
go stale, and a manual edit is not *detected* — it is impossible, because the
next run overwrites it. This is a stronger guarantee than the diff-based
freshness test it replaces, obtained by removing a mechanism rather than adding
one.

**Validation moves earlier.** CI builds the graph and runs the validators
against the fresh graph. A validation `error` fails the build. Nothing compares
against a stored copy.

**Consumers regenerate.** Any consumer — dashboard, assistant, portal — invokes
the generator or reads the CI artifact. No consumer reads a file from the
working tree.

**The specification's freshness test changes shape**, from "diff against
committed" to "build and validate." That is an implementation consequence of
this decision, not a new architecture.
