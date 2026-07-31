# Platform Knowledge Graph — Implementation Specification

**Execution contract.** Conforms to the approved PKG design; redefines nothing.
Every statement ends in executable work.

---

## 0. Requirements that could not be implemented exactly as designed

**All resolved.** §0.1 and §0.3 are constraints with prescribed actions. §0.2
was resolved by Amendment A2 (existing authority). No open decision remains.

### 0.1 `cmd/kg/` fails the build on creation
`registeredBinaries` in `internal/arch/wiring_test.go` contains exactly one
entry (`controlplane`). `TestOperationalBinariesAreRegistered` fails until `kg`
is registered with a safety justification.
**Required:** register `"kg": "read-only derivation over the working tree; opens no database connection and writes only pkg.json"` in the same commit as `cmd/kg/main.go`.

### 0.2 E2 (route extraction) — RESOLVED by Amendment A2
`Router()` returns `http.Handler` and is not walkable by `chi.Walk`; only the
package-private `routes()` is. **Resolution:** the route inventory is derived
inside `internal/httpapi`, following the OpenAPI contract-guard precedent, and
exported for `internal/kg` to consume. See Amendment A2. No AST route inventory
may exist.

### 0.3 The extractors will be scanned by the guards they describe
**Nine guards sweep the whole tree** via `goFilesUnder(t, "..")`. `internal/kg`
enters all of them. An extractor containing a literal table name in Go source
will be read as a real mutation or a real audit-table reference and **fail the
build**.
**Required rule, binding on all extractor code:**
- No `§6.2` table name (`app_user`, `organization`, `membership`, `invitation`,
  `tenant`) and no audit table name (`audit_event`, `admin_audit_event`,
  `*_chain_head`) may appear as a **literal** in non-test Go source under
  `internal/kg`.
- Table names are **derived at runtime** from the migration corpus — never
  declared as constants. *(This is what the design requires anyway; the guard
  constraint and the design agree.)*
- Fixtures live in `testdata/` as `.sql`/`.txt` **data files**, never as Go
  string literals.
- No type in `internal/kg` may declare `Run(ctx context.Context) error` — that
  signature is claimed by `TestEveryWorkerRunLoop_ObservesCancellation`. Use
  `Build(ctx)`.

---

## Part I — Project Structure

```
cmd/kg/main.go                  CLI entry; flag parsing only, no logic
internal/kg/
  graph/                        Node, Edge, Graph; pure data + invariants
  model/                        Origin, Confidence — the shared vocabulary
  extract/                      Extractor interface + registry
    gopkg/  route/  schema/  migration/  guard/  worker/
    openapi/  pipeline/  adr/  doc/  declare/
  parser/                       shared AST/SQL/markdown helpers
  pipeline/                     stage orchestration
  validate/                     validators (Part VI)
  query/                        typed accessors over a loaded graph
  storage/                      pkg.json read/write, schema version, diff
  output/                       human and JSON renderers
.pkg/
  owners.yaml  capabilities.yaml            declarations (Origin=Declared)
testdata/kg/                    golden graphs + extractor fixtures
```

| Package | May import | **Forbidden** |
|---|---|---|
| `model` | stdlib only | everything else, including every other `internal/kg` package |
| `graph` | stdlib, `model` | every other `internal/kg` package; every platform package |
| `parser` | stdlib, `go/ast`, yaml | `extract`, `pipeline` |
| `extract/*` | `graph`, `model`, `parser` | `validate`, `query`, `output`, other extractors |
| `pipeline` | all of the above | `output` |
| `validate` | `graph`, `model` | `extract` |
| `query`, `output` | `graph`, `model`, `storage` | `extract`, `validate` |
| `storage` | `graph`, `model` | everything else |
| `cmd/kg` | `pipeline`, `query`, `output`, `storage` | `extract` directly |

`model` is the sink of this DAG: it owns the shared vocabulary — `Origin` and
`Confidence` — and imports nothing. `graph` owns the topology and its
invariants, and depends on the vocabulary its fields are typed by. The edge
runs `graph → model` and never the reverse, so a cycle between them is
unrepresentable. The prohibition both packages carry is against the *platform*:
neither may import a package outside `internal/kg`.

**The route inventory is exported by `internal/httpapi`** and consumed by
`internal/kg/extract/route` (Amendment A2). The dependency runs
`internal/httpapi → internal/kg`, never the reverse. **No extractor may import
a platform package, and no AST route inventory may exist** — a second route
list that drifts silently is what `server.go:149-152` rejects.

---

## Part II — Domain Model

```go
type Origin string   // "derived" | "declared" | "imported" | "inferred" | "ratified"
type Confidence string // "certain" | "high" | "medium" | "low" | "unknown"

type Evidence struct {
    Source string `json:"source"`           // repo-relative path
    Line   int    `json:"line,omitempty"`
    Rule   string `json:"rule"`             // extractor id, e.g. "E3.table"
}

type Node struct {
    ID         string            `json:"id"`   // "<kind>:<identity>", stable
    Kind       string            `json:"kind"` // package|route|table|guard|adr|...
    Attrs      map[string]string `json:"attrs,omitempty"`
    Evidence   []Evidence        `json:"evidence"`
    Origin     Origin            `json:"origin"`
    Confidence Confidence        `json:"confidence"`
}

type Edge struct {
    From       string     `json:"from"`
    To         string     `json:"to"`
    Kind       string     `json:"kind"` // imports|serves|guards|governs|owns|cites|...
    Evidence   []Evidence `json:"evidence"`
    Origin     Origin     `json:"origin"`
    Confidence Confidence `json:"confidence"`
}

type Graph struct {
    SchemaVersion int     `json:"schema_version"` // start at 1
    Commit        string  `json:"commit"`         // git rev-parse HEAD
    Nodes         []Node  `json:"nodes"`          // sorted by ID
    Edges         []Edge  `json:"edges"`          // sorted by From,To,Kind
}
```

**Invariants, enforced in `graph`:** every Node has ≥1 Evidence unless
`Origin=declared` · every Edge's endpoints exist · IDs unique · collections
sorted before emit.

**Versioning:** `SchemaVersion` bumps on any breaking field change; `storage`
refuses to load a newer version.

---

## Part III — Extraction Engine

```go
type Extractor interface {
    ID() string                                    // "E3"
    Extract(ctx context.Context, root string) ([]Node, []Edge, error)
}
```

| ID | Input | Algorithm | Failure mode | Perf |
|---|---|---|---|---|
| E1 gopkg | `go list -json ./...` | decode; node per package; edge per import | `go list` non-zero → **fatal** | <2 s |
| E2 route | route inventory exported by `internal/httpapi`, derived there by `chi.Walk` over `routes()` (Amendment A2) | node per method+path; edge route→handler, route→middleware | inventory empty → **fatal** | <100 ms |
| E3 schema | `migrate/sql/*.sql` | regex per object kind; tables, columns, constraints, indexes, triggers | unparsed statement → **warn**, continue | <200 ms |
| E4 migration | filenames + `atlas.sum` | ordered lineage; pair against `rollback/` | missing pair → **node attr**, validator reports | <50 ms |
| E5 guard | AST of `internal/arch` | node per `func Test*`; attrs: sweeps-tree, has-antivacuity | parse error → **fatal** | <300 ms |
| E6 worker | AST + SQL | `Run(ctx)` receivers; `FOR UPDATE SKIP LOCKED` sites; lease columns | — | <300 ms |
| E7 openapi | `openapi.yaml` | indentation parse (mirrors `contract_test.go`) | malformed → **fatal** | <100 ms |
| E8 pipeline | `ci.yml`, `Makefile` | jobs, targets, gate commands | — | <50 ms |
| E9 adr | `docs/adr/*.md` | header table → attrs; regex `path:line` in prose → cites edges (**Confidence=medium**) | no header → `origin=imported, confidence=low` | <200 ms |
| E10 doc | `docs/**/*.md` | claims: counts, file references | — | <200 ms |
| E11 declare | `.pkg/*.yaml` | owners, capabilities | invalid yaml → **fatal**; unknown target → validator | <10 ms |

**Determinism:** no network, no clock, no env reads. Sort before return.
**Every extractor: golden test + a fixture with a deliberately malformed input.**

---

## Part IV — Graph Pipeline

| Stage | In | Out | On error |
|---|---|---|---|
| Scan | root | file lists | fatal |
| Parse | files | ASTs/trees | fatal on Go/YAML, warn on SQL |
| Extract | trees | nodes/edges per extractor | extractor-declared |
| Normalize | raw | canonical IDs, dedup | fatal on ID collision with differing attrs |
| Link | nodes/edges | resolved edges | unresolved → dropped + recorded |
| Validate | graph | findings | never fatal; findings are output |
| Score | graph | confidence assigned | fatal on rule violation (Part V) |
| Serialize | graph | bytes | fatal |
| Diff | *(removed by ADR-PKG-001 — nothing is compared against a stored copy)* | — | — |
| Publish | bytes | `pkg.json` | fatal on write error |

Extractors run **concurrently**; the pipeline is otherwise sequential.

---

## Part V — Confidence Engine

Origin and Confidence are **independent fields**, assigned by table — never
computed ad hoc:

| Origin | Max Confidence |
|---|---|
| derived (executable artifact) | **certain** |
| ratified (ADR header) | high |
| declared (`.pkg/`) | high |
| imported (structured doc) | medium |
| inferred (prose heuristic) | **medium** |

**Enforced rule — `scorer.Validate()` returns an error, pipeline aborts:**
```
origin == "inferred"  =>  confidence != "certain"
```
No inferred node may become Certain. Downgrade only: an edge's confidence is
`min(endpoints, edge)`.

---

## Part VI — Validators

Each implements `Validate(g *Graph) []Finding`; `Finding{Rule, Severity, NodeID, Message, Evidence}`.
Severity: `error` (certain-confidence defect, fails CI) · `warn` (medium) · `info`.

`broken-reference` · `duplicate-authority` · `missing-evidence` · `orphan-adr` ·
`unknown-owner` · `capability-drift` · `inventory-drift` · `graph-freshness` ·
`confidence-violation` · `documentation-drift`

Only `confidence-violation`, `broken-reference` and `graph-freshness` are
`error` at M3. Others begin as `warn` and are promoted per capability.

---

## Part VII — Storage

`pkg.json` is **generated, never committed** (ADR-PKG-001). It is written to
the repository root at build time and **excluded by `.gitignore`**; CI
regenerates it on every run and publishes it as a build artifact. A generated
artifact is never canonical repository authority.

Indented 2 spaces, trailing newline, keys sorted — byte-stable across runs, so
two runs on the same tree produce identical bytes. `kg diff` compares two
generated graphs supplied by the caller, not a stored copy. **No incremental
regeneration** — full rebuild is under 10 s; incremental is a
cache-invalidation defect waiting to happen.

---

## Part VIII — CLI

| Command | Exit 0 | Exit 1 | Exit 2 |
|---|---|---|---|
| `kg build` | wrote pkg.json | extraction failed | — |
| `kg validate` | no `error` findings | `error` findings | — |
| `kg diff` | no change | changed | — |
| `kg validate --regenerate` | no `error` findings | `error` findings | — |
| `kg query <sel>` | results (may be empty) | bad selector | — |
| `kg export --format=dot\|json` | ok | fail | — |
| `kg doctor` | all validators ran | any `error` | — |
| `kg stats` | printed | — | — |

Global: `--json` (machine output), `--root`, `--quiet`. Human output is a table;
`--json` is the `Graph`/`[]Finding` structs verbatim.

---

## Part IX — CI

**Make targets:** `graph` (build) · `graph-check` (build + validate).
**CI:** add `graph-check` to the existing `build-test` job — no new job.
**Regeneration test:** `internal/kg/pipeline/regenerate_test.go` builds the
graph and runs the validators against it. A validation `error` fails the build.
Determinism is asserted separately by building twice and comparing the two
generated results. Nothing is compared against a stored copy (ADR-PKG-001).
**Performance budget:** full build < 10 s, asserted by a benchmark that fails
over budget.

---

## Part X — Test Strategy

**Unit** per extractor. **Golden**: `testdata/kg/golden/*.json`, regenerated by
`-update`. **Property**: build twice → byte-identical (determinism). **Fixture**:
each extractor gets one malformed input asserting its declared failure mode.
**Integration**: full pipeline over the real repo; asserts node counts per kind
are non-zero. **Mutation** (M3): remove the `inferred≠certain` rule · drop a
validator · break sorting · skip an extractor — **each must fail the build**.
**Regression**: every defect found post-merge gains a fixture.

---

## Part XI — Milestones

| M | Deliverable | Rollback | Exit |
|---|---|---|---|
| **M1** | `graph`, `model`, `storage`, E1, `kg build` | delete package; nothing depends on it | `pkg.json` with packages+imports; deterministic |
| **M2** | E3, E4, E5, E6, E7, E8 | drop extractor from registry | all derived nodes present; golden tests pass |
| **M3** | `validate` + Part V scorer + mutations | validators warn-only | confidence rule enforced; mutations kill |
| **M4** | full CLI + `query` + `output` | CLI only; graph unaffected | all 8 commands, exit codes as specified |
| **M5** | `make graph-check` in CI + regeneration test | remove target | validation `error` fails CI |
| **M6** | E9, E10, E11 + `.pkg/` declarations | delete declarations | ownership queries answerable |

Each milestone is independently deployable; M1–M5 need no human input.
**M6 is last** because it is the only milestone requiring curation.

---

## Part XII — Acceptance

Repository regenerates in < 10 s · two consecutive builds byte-identical ·
`pkg.json` is git-ignored and absent from the index · a validation `error` fails CI · `unknown-owner` reports **18 packages at M6** ·
broken ADR references detected with file:line · no node has
`origin=inferred, confidence=certain` · mutation suite fully killing ·
`cmd/kg` registered in `registeredBinaries` · no §6.2 or audit table name
appears as a literal in `internal/kg` non-test source · all existing gates green.

---

*Implementation contract. Derives authority from the approved PKG design.*

---

# Amendment A1 — Determinism covers tool-emitted paths *(resolves EAR B2)*

**Type:** Specification Amendment. Changes the determinism model; therefore
architectural, per the Engineering Law.

§III's determinism rule reads: *"no network, clock, or environment."* Repository
evidence shows the rule does not cover the actual failure — `go list -json`
emits machine-specific absolute paths:

```
"Dir":  "/Users/…/oneops/internal/domain"
"Root": "/Users/…/oneops"
```

Passed through, `pkg.json` becomes machine-specific.

**Amended rule.** *No extractor may emit a value that varies with the machine,
the checkout location, the network, the clock or the environment. Any path a
tool returns is normalised to a repository-relative path before it enters a
Node, an Edge or an Evidence record. The repository root is the only anchor.*

**Canonical form:** forward-slash separated, relative to the repository root, no
leading `./`. The normalisation algorithm is implementation, not architecture.

**Consequence:** applies to every extractor, not only E1. Any future extractor
consuming a tool that reports absolute paths inherits it.

---

# Amendment A2 — Route extraction uses existing authority *(resolves EAR B1)*

**Type:** Specification Amendment. Records an existing decision; **no ADR is
required**, per the Engineering Law.

§0.2 presented two options and recommended one. The repository had already
decided. `internal/httpapi/server.go:149-152`:

> *"routes builds the mux Router serves. It is separate from Router only so the
> route table stays reachable as a chi.Routes: the OpenAPI contract guard walks
> it to derive its subject set, and the alternative — restating the routes in a
> test — is a second list that drifts from this one silently."*

The separation of `routes()` from `Router()` exists **specifically so a
derivation tool may walk the route table.** Option (a) — walking the constructed
router — is ratified by that comment. Option (b), AST extraction, is the
"second list that drifts" the comment rejects.

**Binding constraints, from evidence:**
- `Router()` returns `http.Handler` (it wraps `routes()` in `otelhttp`), so it
  is **not** walkable by `chi.Walk`. Only `routes()` is.
- `routes()` is package-private, as are the `newFakeRepo`/`newFakeIdem` fakes
  the proven pattern uses, and `idempotencyStore` is an unexported interface.

**Therefore:** the route extractor follows the precedent already set by the
OpenAPI contract guard and **lives inside `internal/httpapi`**, exposing an
exported route inventory that `internal/kg/extract/route` consumes. §I has been
reconciled to state this direction; the dependency runs `internal/httpapi →
internal/kg`, never the reverse.

**Consequence:** no change to `Router()`'s signature, no export of `routes()`,
and no new coupling from `internal/kg` into `config`, `auth` or `observability`.

---

# Amendment A3 — Vocabulary ownership, edge serialisation, and freshness *(resolves S1.1 blockers C1, C2, C3)*

**Type:** Specification Amendment. C1 amends the package dependency rule and is
**architectural**. C2 and C3 are **editorial** — they correct text that
contradicts obligations the specification already imposes — and are ratified
here only because the baseline is frozen.

**Ratified by:** Architecture Council Session A3.

## C1 — Part I, package dependency table

**Superseded text** (§I, dependency table):

```
| `graph`, `model` | stdlib only | anything in `internal/` |
```

**Replacement text** — two rows, plus the paragraph now following the table:

```
| `model` | stdlib only | everything else, including every other `internal/kg` package |
| `graph` | stdlib, `model` | every other `internal/kg` package; every platform package |
```

**Rationale.** §I assigns `Origin` and `Confidence` to `model`; §II types
`Node`'s and `Edge`'s fields with them; §I assigns `Node` and `Edge` to `graph`.
§I therefore entails an import that §I forbids. The grouped subject cell in the
superseded row expressed a shared constraint against the *platform*, not mutual
isolation between two packages that must be layered: it is the only row in the
table whose subject groups two packages while its Forbidden column uses a
blanket phrase rather than naming siblings. Splitting the row states the
intended constraint and leaves every other row correct.

**Canonical DAG.** `model` is the sole sink. `parser` remains an independent
leaf. `model` may never import `graph`, so a cycle is unrepresentable.

```
                          model            <- stdlib only; imports nothing
                            ^
                          graph            <- stdlib + model
                            ^
      +----------+----------+----------+----------+
   storage    validate   extract/*    query     output
      ^                      ^          ^          ^
      |                   parser        |          |
      |                  (stdlib,       |          |
      |                 go/ast, yaml)   |          |
      +----------+-----------+----------+----------+
              pipeline        (all of the above except output)
                  ^
               cmd/kg         (pipeline, query, output, storage)
```

## C2 — Part II, `Edge` declaration

**Superseded text** (§II):

```go
    From, To   string     `json:"from","to"`
```

**Replacement text:**

```go
    From       string     `json:"from"`
    To         string     `json:"to"`
```

**Canonical declaration, ratified in full:**

```go
type Edge struct {
    From       string     `json:"from"`
    To         string     `json:"to"`
    Kind       string     `json:"kind"` // imports|serves|guards|governs|owns|cites|...
    Evidence   []Evidence `json:"evidence"`
    Origin     Origin     `json:"origin"`
    Confidence Confidence `json:"confidence"`
}
```

**Rationale.** A Go struct tag applies to every name in its declaration. Both
fields resolved to the JSON name `from`; `encoding/json` treated the collision
as a conflict and dropped both, marshalling a fully populated edge to
`{"kind":"imports"}` and round-tripping its endpoints to empty strings. The
corrected form changes no obligation: §II already requires every edge's
endpoints to exist, §II already sorts edges by `From,To,Kind`, and §VII already
requires a byte-stable `pkg.json`.

## C3 — Part I, `model/` contents

**Superseded text** (§I, project structure):

```
  model/                        entity types, Confidence, Origin, Freshness
```

**Replacement text:**

```
  model/                        Origin, Confidence — the shared vocabulary
```

**Rationale.** `Freshness` was named once in this specification and defined
nowhere. The PKG Design §6 realises freshness as commit distance, which §II
implements as `Graph.Commit`; under ADR-PKG-001 no node can carry a freshness
differing from its graph's, because the graph is regenerated wholesale and
never stored. "Entity types" likewise named nothing §II defines — packages,
routes and tables are `Node.Kind` string values, not Go types. Both phrases are
struck so that no implementer creates types to satisfy a gloss.

**No `Freshness` type exists.** Freshness is represented by `Graph.Commit`.

## Affected sections

| Section | Change |
|---|---|
| §I — project structure | C3 replacement |
| §I — dependency table | C1 replacement (1 row -> 2) + explanatory paragraph |
| §II — `Edge` | C2 replacement (1 line -> 2) |

§§III–XII are unaffected. Amendments A1 and A2 are unaffected. ADR-PKG-001 is
unaffected and is load-bearing for C3.

**Consequence for the backlog:** S1.1's acceptance criterion "stdlib-only
imports" transcribed the superseded row and is corrected to state the layered
rule. No story is added, removed, resequenced or re-pointed.
