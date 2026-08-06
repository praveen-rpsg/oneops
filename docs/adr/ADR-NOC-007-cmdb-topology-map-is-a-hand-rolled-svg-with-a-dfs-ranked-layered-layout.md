# ADR-NOC-007 — The CMDB topology map is a hand-rolled SVG with a DFS-ranked layered layout and a client-side incident/health overlay

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | ADR-NOC-006 (the `GET /v1/admin/assets/graph` projection this screen renders), ADR-UI-001 (Cloudscape + Vite console foundation, the self-containment bar this story must keep meeting), ADR-NOC-004/ADR-NOC-005 (the `SplitPanel` drill-down pattern this story reuses), `docs/PLATFORM-BUILD-PLAN.md` E7.3b-2 |

## Context

E7.3b split the CMDB topology map into a Go-endpoint story (E7.3b-1,
ADR-NOC-006) and this UI story. ADR-NOC-006 deliberately returns a raw,
unlaid-out `{nodes, edges, truncated}` projection and explicitly assigns
"layout, pan/zoom and rendering" to this story, together with composing the
incident/health overlay client-side from the existing
`GET /v1/admin/incidents` and `GET /v1/admin/assets/health` endpoints. The
brief is explicit that no viz library may be added — the whole rendering
surface, including layout, is hand-rolled SVG inside the existing Cloudscape
shell (ADR-UI-001).

A tenant's asset graph is not guaranteed to be a DAG: `asset_relationship`
has no acyclicity constraint, and ADR-NOC-006 §3 explicitly does not filter
retired CIs out of edges, so a topology map must render whatever the CMDB
actually contains — including a relationship cycle a human mis-modelled — and
must never hang doing so.

## Decision

### 1. No viz library — a small, hand-rolled layout engine plus raw SVG

`web/src/topologyLayout.ts` computes layout; `web/src/components/TopologyGraph.tsx`
renders it as plain SVG elements (`<rect>`, `<text>`, `<line>`, `<marker>`)
with pan/zoom driven by mutating the SVG's own `viewBox`. No d3, cytoscape,
vis-network, or any other graph/charting package is a dependency of this
story (verified: `web/package.json`'s dependency list is unchanged by this
change). This mirrors the same posture ADR-UI-001 already committed to for
the console as a whole — Cloudscape plus hand-written code, no incidental
libraries — extended to a graph-drawing problem instead of a form/table
problem.

### 2. Layered layout, ranked by a DFS longest-path computation over dependency edges

`AssetGraphEdge.from_asset_id`/`to_asset_id` means "from depends on to"
(ADR-NOC-006's own vocabulary: `depends_on`/`runs_on`/`connected_to`/
`member_of`). `computeTopologyLayout` ranks every node by dependency depth:
a node with no outgoing edges (nothing it depends on, ignoring cycles — see
§3) ranks `0`; a node that depends on others ranks one above the highest
rank among what it depends on. Nodes are grouped into horizontal layers by
rank and rendered top-down — a dependent renders above the things it depends
on, so the shape reads the way a person would draw it by hand (front end on
top, database at the bottom).

Determinism is structural, not incidental: node ids and edges are sorted
(`asset_id`, then `from_asset_id`/`to_asset_id`/`type`) before any traversal,
so the DFS visits nodes in the same order every time, layer membership is the
same set every time, and position within a layer is the same alphabetical
order every time. The same `{nodes, edges}` response always produces the
same layout — no random seed, no insertion-order dependency, nothing that
would make two people looking at the same tenant's map at different times see
a different picture for no reason.

### 3. Cycle-tolerant by construction: white/gray/black DFS colouring, never a visited-set that recurses into gray

The classic three-colour DFS (white = unvisited, gray = on the current
recursion stack, black = finished) is what makes termination provable rather
than merely likely:

- A node turns gray the instant its DFS call begins and black only once every
  child has been visited.
- An edge to a **gray** node is a **back edge** — it closes a cycle onto an
  ancestor still being visited. It is recorded (`isBackEdge: true`) and the
  algorithm explicitly does **not** recurse into it. Recursing into a gray
  node is exactly the infinite loop this design exists to prevent; skipping
  it is what keeps the traversal to one visit per node.
- An edge to a **black** node reuses its already-computed rank in O(1)
  instead of re-deriving it.
- A self-loop (`from_asset_id === to_asset_id`) is handled by the same rule
  with no special case: a node is gray to itself the instant its own DFS
  call starts, so its own self-edge is immediately classified as a back edge.

Because every node transitions white → gray → black exactly once and a back
edge is detected instead of followed, the whole computation is
`O(nodes + edges)` regardless of how many cycles — or how large one — the
input contains. A back edge is still **drawn** (dashed, in the palette's
"back edge" color, with its own arrowhead marker) so the cycle is visible to
the operator, but it never influences any node's rank or vertical position.

This is proven, not asserted: `topologyLayout.test.ts`'s
`is cycle-tolerant and terminates` test lays out a three-node cycle
(`A -> B -> C -> A`) under a wall-clock bound, asserts exactly one edge is
classified as a back edge, and asserts every node still receives a finite,
non-negative rank; a second test does the same for a bare self-loop.
`routes/TopologyPage.test.tsx`'s `renders a cyclic graph without hanging`
test repeats the same fixture through the full page (fetch → layout → SVG
render) end-to-end.

### 4. The graph is not joined with incident/health state — the overlay is merged client-side, by design (ADR-NOC-006 §4)

`web/src/topologyOverlay.ts`'s `buildTopologyOverlay` takes the incidents
list (`GET /v1/admin/incidents`, no status filter, so both open and
terminal-status incidents are fetched once) and the health report
(`GET /v1/admin/assets/health`) and produces one `Map<asset_id, NodeOverlay>`:

- An incident counts toward a node's overlay only when it carries an
  `asset_id` (an unlinked incident contributes nothing — the same "Unlinked"
  case the incident board already renders explicitly, E7-UI.2) **and** its
  status is "open-class" per `incidentPresentation.ts`'s own
  `isOpenIncidentStatus` (everything ranked before `resolved` in
  `STATUS_RANK`: open, acknowledged, investigating, reopened). A resolved or
  closed incident never colors a node.
- A health-report sample (`stale`/`orphaned_assets`/
  `orphaned_business_services`/`incomplete`) marks a node with a named health
  issue. `AssetHealthCategory.samples` is bounded (ADR-NOC-006's own sibling
  endpoint, E1.5) — a tenant whose affected-asset count exceeds the sample
  bound will have some true positives this overlay cannot see. This is the
  same honest limitation the health report itself already carries; the map
  does not claim to see more than its source data does.
- An open incident always outranks a health issue on the same node (status
  `'incident'` wins over `'health'`): an active incident is the more urgent
  signal an operator needs first.
- Either source failing to load degrades independently — the overlay for
  that source is simply empty, not a blocking error — following the same
  "degrade a supplementary fetch without blanking the view" precedent
  `OnCallBoardPage` established in E7.3c. The graph itself (the primary data)
  still renders with a neutral palette if both overlay sources fail.

This composition happens once, in the page component, from data the graph
render never needs a second round-trip for — reconciliation by `asset_id` is
a `Map` lookup per node, not a fetch per node.

### 5. Pan/zoom is a `viewBox` transform, not a DOM/CSS transform library

`TopologyGraph` keeps `scale` and `pan: {x, y}` in component state and
computes `viewBox="${pan.x} ${pan.y} ${width/scale} ${height/scale}"` on
every render. Panning is a background mouse-drag converted from client
pixels to SVG user units via `getBoundingClientRect()`; zoom is two
Cloudscape icon buttons (`zoom-in`/`zoom-out`) plus a `zoom-to-fit` reset, and
an optional mouse-wheel handler attached as a **native**, non-passive
listener (`{ passive: false }`) specifically because React's synthetic
`onWheel` is passive by default and calling `preventDefault` inside a passive
listener throws — the native listener is the correct fix, not a workaround.
A node's own `onMouseDown` calls `stopPropagation()` so a click-to-select
never also starts a background drag.

### 6. The palette is a fixed, hand-written light/dark pair — not `@cloudscape-design/design-tokens`

Raw SVG attribute colors cannot consume Cloudscape's own component CSS
directly: every color custom property `@cloudscape-design/components` emits
is scoped per component with a build-generated hash suffix (e.g.
`--color-text-status-error-bwp1cm` in the installed `3.0.1341`), and
`@cloudscape-design/design-tokens` — the first-party package that gives
those same colors stable, importable names — is **not** a dependency of this
project (checked: absent from `web/package.json` and from the resolved
`pnpm` store; nothing in `@cloudscape-design/components`'s own dependency
tree pulls it in transitively). Adding a new pinned dependency, and a second
CSS import (`design-tokens` ships its own `index.css` alongside the JS
constants), for one screen's fill colors was judged a worse trade than a
small, explicit palette.

`web/src/topologyPresentation.ts`'s `LIGHT`/`DARK` palettes hand-encode the
same semantic split every `StatusIndicator` in this console already uses
(ADR-NOC-002/003, carried forward by `incidentPresentation.ts`'s
`SEVERITY_TYPE`/`STATUS_TYPE`): red for an open incident, amber for a health
issue, a neutral grey/blue otherwise. `useTopologyMode` (same file) tracks
the live theme by observing `document.body`'s `awsui-dark-mode` class via a
`MutationObserver` — the exact class `theme.ts`'s `applyMode` toggles — so
the map re-colors immediately when the user flips the shell's dark-mode
switch, without `TopologyPage` needing a second, parallel theme channel from
`Shell`.

**What this does not do.** It does not add `@cloudscape-design/design-tokens`
as a dependency (a fair follow-up if a second hand-rolled-graphics screen
ever needs the same tokens, at which point sharing one properly-versioned
palette source outweighs a second hand-copied one). It does not attempt to
minimize edge crossings or otherwise optimize layer-internal ordering beyond
the deterministic alphabetical sort — acceptable for the graph sizes this
screen realistically renders (bounded at 2000 nodes/5000 edges by
ADR-NOC-006, and no real tenant is anywhere near that yet); a Sugiyama-style
crossing-minimization pass is a fair follow-up if a large, dense tenant graph
is ever hard to read in practice.

## Alternatives considered

- **A viz library (d3-force, cytoscape.js, vis-network).** Rejected per the
  story's own hard constraint and consistent with ADR-UI-001's Cloudscape-
  plus-hand-written-code posture: any of these is materially heavier than
  this screen's needs (a bounded, mostly-tree-shaped dependency graph, not an
  arbitrary force-directed layout problem) and would be this console's first
  runtime dependency whose update cadence and bundle-size behavior this team
  does not otherwise track.
- **A force-directed or radial layout instead of layered/hierarchical.**
  Rejected: a dependency graph reads naturally as "what depends on what,
  drawn top-down" — the same mental model an operator already uses when they
  say "the database is downstream of the API." A force-directed layout is
  non-deterministic run-to-run (or requires a fixed random seed to fake
  determinism) and answers a different question (community structure) than
  the one this screen needs to answer (blast radius up/down a dependency
  chain).
- **Reject cyclic input outright / show an error state for a cyclic graph.**
  Rejected: `asset_relationship` has no constraint against a relationship
  cycle, ADR-NOC-006 does not filter for one, and a CMDB with a genuine
  mis-modelled cycle (or a legitimate mutual `connected_to` pair) is exactly
  the kind of CMDB hygiene issue this map exists to surface, not hide behind
  an error screen.
- **Ranking from the "root" side (in-degree 0) via forward propagation
  instead of a DFS longest path from the leaf side.** Both are valid DAG
  layering strategies; the DFS-longest-path-from-leaves formulation was
  chosen because the white/gray/black cycle detection composes with it in one
  pass with no separate topological-sort/Kahn's-algorithm step, keeping the
  cycle-tolerance guarantee and the ranking computation the same piece of
  code rather than two coordinated ones.

## Consequences

**What is now guaranteed.** `/topology` renders the tenant's whole CMDB
dependency graph from `GET /v1/admin/assets/graph` (ADR-NOC-006) as a
deterministic, top-down layered SVG; a node carrying an open incident renders
red, a node with only a CMDB health issue renders amber, otherwise neutral —
composed client-side from `GET /v1/admin/incidents` and
`GET /v1/admin/assets/health`, matched by `asset_id`. A relationship cycle of
any size or a self-loop never hangs the layout computation or the page — the
DFS visits each node exactly once and a back edge is drawn, never followed.
Panning and zooming are `viewBox`-only, requiring no additional rendering
library. Clicking a node opens its full detail (name, type, status,
environment, criticality, open-incident count, health issues) in the shell's
existing `SplitPanel`, reusing `ShellSplitPanelContext` with no new
per-screen drill-down mechanism. `graph.truncated` renders a Cloudscape
`Alert`; an empty tenant renders a clean empty state naming the two calls
(`POST /v1/admin/assets`, `POST /v1/admin/assets/relationships`) that would
populate it. The bundle remains self-contained: no new npm dependency, no new
runtime CDN reference (`make web`'s grep check against the built bundle is
unchanged in what it reports).

**What is not claimed.** This is not a general-purpose graph-visualization
component — it has no crossing-minimization, no force simulation, no
edge-bundling, and is tuned for the tree-like shape a dependency graph
usually has, not an arbitrarily dense one. The health overlay can miss a true
positive beyond `AssetHealthCategory.samples`'s bound, an existing, named
limitation of the endpoint this story reuses rather than a new one. Dark/
light re-theming for the hand-rolled SVG depends on observing a DOM class
Cloudscape's own `global-styles` package happens to set today
(`awsui-dark-mode` on `document.body`); a future Cloudscape major version
changing that mechanism would need this file's `useTopologyMode` updated
alongside it — an accepted, narrow coupling in exchange for not adding a new
theming dependency for one screen.

## Enforcement

- `topologyLayout.test.ts` — the ranking, layering, determinism, missing-
  edge-filtering and (critically) the cycle/self-loop termination guarantees,
  proven directly against the pure layout function, including a wall-clock
  bound on the cyclic-graph test so a future regression that reintroduces
  unbounded recursion fails fast instead of hanging the test runner.
- `topologyOverlay.test.ts` — the incident/health merge rules: open-class
  only, unlinked incidents ignored, incident outranks health on the same
  node, either source degrading independently.
- `routes/TopologyPage.test.tsx` — the end-to-end page: every node/edge
  rendered from a mocked graph, red/amber coloring verified through the
  `SplitPanel` detail text, the cyclic-graph-does-not-hang case repeated
  through the full fetch→layout→render path, truncation alert, empty state,
  and overlay-source-failure degradation.
- `make web` — self-containment: `grep -o 'https\?://[^"'"'"']*'` against the
  built `webdist/assets/*` still returns only the same inert strings
  ADR-UI-001 already accepted (SVG/XML namespace URIs, the React
  error-decoder doc link, an Open Sans license comment) — no new external URL
  from this story's code or from any new dependency, because none was added.
- `go build ./...` / `make test` — untouched; this is a `web/`-only story, no
  Go source, migration, or `internal/arch` guard is touched by it.
