# OneOps Platform Knowledge Graph (PKG) — System Design

**Status:** Design. Not constitutional. Implements the three manual rows the
Engineering Operating System names as its own backlog: repository inventory,
dependency graph, ADR validation.

---

## 1. Vision

The repository is the only writable source of architectural truth. Every
architectural artifact — inventory, dependency graph, ownership, traceability —
is **regenerated from executable evidence** and fails CI when stale.

### One correction to the stated premise, from measurement

The brief requires *"nothing should be manually curated."* **Three entities are
not derivable from any executable artifact, and no extractor can invent them:**

| Entity | Evidence | Why not derivable |
|---|---|---|
| **Ownership** | no `CODEOWNERS`, 0 owner annotations, 2 git authors | Nothing in the tree expresses who owns a package |
| **Capability identity** | no marker groups packages into capabilities | "Capability" is a human abstraction over packages |
| **Hypothesis vs fact** | prose only | Intent cannot be extracted from code |

The principle survives in its stronger form: **the repository remains the only
writable source** — declarations live *in* the repo as data. What changes is the
claim of zero curation.

> **Declare intent once. Derive everything else forever.**

A design asserting full derivation of ownership would produce a confident,
wrong graph — the failure Constitution Law 12.1 forbids.

---

## 2. Ontology

Every node carries: **identity · attributes · relationships · evidence link ·
confidence · freshness**.

| Node | Identity | Derived from | Confidence |
|---|---|---|---|
| Package | import path | `go list` | **Certain** |
| Route | method + path | `chi.Walk` over the live router | **Certain** |
| Migration | filename | `migrate/sql/` | **Certain** |
| Table / Column / Constraint / Index / Trigger | name | migration SQL | **Certain** |
| Guard | test func name | `internal/arch/` AST | **Certain** |
| Queue | table + claim site | SQL + store AST | **Certain** |
| Worker | `Run(ctx)` receiver | AST | **Certain** |
| CI job / Make target | name | YAML / Makefile | **Certain** |
| ADR | file id | `docs/adr/` header table | **High** (30/37 structured) |
| ADR→artifact edge | citation | regex over prose `file:line` | **Medium** |
| Runbook / Blueprint | file | `docs/` | **Medium** |
| **Capability** | declared id | `.pkg/capabilities.yaml` | **Declared** |
| **Ownership** | declared team | `.pkg/owners.yaml` | **Declared** |
| **Hypothesis / Decision** | declared | ADR + blueprint front-matter | **Declared** |

**Confidence tiers.** `Certain` — deterministic from an executable artifact.
`High` — structured text. `Medium` — heuristic over prose; must be labelled.
`Declared` — human input, versioned in the repo.

**A `Medium` or `Declared` fact may never be rendered as `Certain`.**

---

## 3. Repository Extractors

One binary, `cmd/pkgraph`, emitting `pkg.json`. Every extractor is
deterministic: same tree in, byte-identical graph out.

| # | Extractor | Source | Yields |
|---|---|---|---|
| E1 | Package | `go list -json ./...` | packages, imports |
| E2 | Route | `chi.Walk` on the constructed router *(pattern already proven by `contract_test.go`)* | routes, guards, handlers |
| E3 | Schema | migration SQL | tables, columns, constraints, indexes, triggers |
| E4 | Migration | filenames + `atlas.sum` | ordered lineage, rollback pairing |
| E5 | Guard | AST of `internal/arch` | guards, and — where derivable — their subject sets |
| E6 | Worker/Queue | AST + SQL | workers, queues, claim sites, lease columns |
| E7 | API contract | `openapi.yaml` | operations, schemas |
| E8 | Pipeline | `ci.yml`, `Makefile` | jobs, targets, gate coverage |
| E9 | ADR | header tables + prose citations | ADR nodes, ADR→ADR, ADR→artifact |
| E10 | Doc | `docs/**` | runbooks, claims for staleness checks |
| E11 | Declaration | `.pkg/*.yaml` | capability, ownership, hypothesis |

**Determinism rule:** no extractor may consult network, clock, or environment.
Sort every collection before emit, or the freshness guard flaps.

---

## 4. Graph Construction

```
extract → normalise → link → validate → score → emit pkg.json  (never committed)
```

Edges are typed and each carries its confidence: `imports · serves · guards ·
mutates · claims · governs · supersedes · owns · implements · cites`.

**The self-maintaining mechanism — the whole design rests on this:**

```
make graph        # regenerate pkg.json
TestGraphRegenerates         # build + validate; build twice, compare results
```

**A stale graph fails CI.** Manual editing is not forbidden by policy; it is
erased on the next regeneration and detected immediately. This is structural
enforcement, per Constitution Law 14.7 — the same pattern as the audit
chokepoint.

---

## 5. Validation Model — Part VIII self-checks, all computable

| Check | Detection |
|---|---|
| Orphan ADR | ADR whose cited artifacts no longer exist |
| Broken reference | any `file:line` citation that does not resolve |
| Unowned package | package absent from `owners.yaml` — **today: all 18** |
| Missing guard | mutating store function outside any guard's subject set |
| Missing rollback | forward migration with no `rollback/` pair |
| Missing test | exported function with no test reference |
| Stale document | doc asserting a count that the graph contradicts |
| Duplicate authority | two documents claiming the same subject |
| Guard without mutation | guard never named in a mutation record |
| Unimplemented blueprint | blueprint section with no corresponding artifact |

**Every finding cites its evidence and confidence.** A `Medium`-confidence
finding is advisory; a `Certain` one is a build failure.

---

## 6. Freshness Model

Freshness is **commit distance**, not wall-clock: a fact is fresh if generated
from the current tree. `pkg.json` records `git rev-parse HEAD`; a mismatch is
stale regardless of age. Declarations carry their own `reviewed_at`, and an
ownership declaration older than two quarters is flagged, never deleted.

---

## 7. Traceability

Answers to the Part VII queries, with today's honest status:

| Query | Answerable | Basis |
|---|---|---|
| Which ADR governs this package? | **Yes** (Medium) | E9 citations |
| Which migrations affect this API? | **Yes** | E3+E7 via table→route |
| Which guards protect this module? | **Yes** | E5 subject sets |
| Which capabilities own these routes? | **After declaration** | E11 |
| Which capabilities are hypotheses? | **After declaration** | E11 |
| Which documentation is stale? | **Yes** | §5 |
| Which blueprint is partially implemented? | Partial (Medium) | E10+E11 |
| Which packages have no owner? | **Yes — answer today: all 18** | E11 absence |
| Which decisions lack evidence? | **Yes** | ADR with no resolving citation |
| Which guards have no mutations? | **Yes** | E5 + mutation records |

---

## 8. AI Integration

`pkg.json` is the retrieval surface: an assistant answers from **derived facts
with citations**, never from prose. Every response carries confidence, so an
assistant cannot present `Medium` as `Certain` — the failure mode that cost this
programme two planning cycles.

**Constraint:** the PKG is read-only to assistants. An agent may propose a
declaration change as a diff; it may never write `pkg.json`.

---

## 9. Query Model

`pkg.json` is a single artifact — packages, routes, tables, guards number in the
hundreds, not millions. **No graph database.** Query via `jq`, a small Go API,
and guard-time assertions. Revisit only if the node count exceeds ~10⁵.

---

## 10. Operational Architecture

No service. No daemon. No storage. A CLI in CI plus a generated, git-ignored artifact.
Runs in the existing `build-test` job; adds seconds. **Nothing to operate is
the design goal** — a knowledge graph requiring HA would be a new capability
competing with the platform it documents.

---

## 11. Implementation Roadmap

| M | Deliverable | Value | Effort |
|---|---|---|---|
| **M1** | E1–E4 + `pkg.json` + `make graph` + freshness guard | Inventory and schema derived; **kills 1 of 3 EOS manual rows** | 2 wk |
| **M2** | E5–E8 | Guards, workers, queues, pipeline derived; **dependency graph automated** — 2nd manual row | 2 wk |
| **M3** | E9 + validation §5 | ADR traceability; orphan/broken-reference detection — 3rd manual row | 2 wk |
| **M4** | E11 declarations: `owners.yaml`, `capabilities.yaml` | Ownership and capability queries become answerable | 1 wk |
| **M5** | Staleness + duplicate-authority checks | Documentation decay becomes a build failure | 1 wk |

**M1–M3 need no human input and retire every manual row in EOS Part V.**

---

## 12. Success Metrics

| Metric | Target |
|---|---|
| Manual rows in EOS Part V | **3 → 0** |
| Facts derived vs declared | ≥ 90% derived |
| Unowned packages | 18 → 0 |
| Broken references | 0 |
| Stale docs detected | trending to 0 |
| Graph regeneration time | < 10 s |
| Hand-maintained lists in guards | trending to 0 |

**The governing metric is the count of hand-maintained lists.** Every one the
PKG replaces is one that can no longer rot — which is the same argument the
constitutional guards already won.

---

*Design derives all authority from the Execution Constitution and the EOS.
Amend freely.*
