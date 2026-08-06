# ADR-HARD-002 — TopologyPage's split-panel content refreshes when the incident/health overlay resolves, instead of freezing a click-time snapshot

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | `docs/PLATFORM-BUILD-PLAN.md` E-HARD.7 (this story), ADR-HARD-001 (the test-determinism audit that found this bug and deliberately left it unfixed as test-only scope), ADR-NOC-007 (the topology screen: layout, hand-rolled SVG, and the client-side incident/health overlay this story makes reactive), ADR-NOC-004/ADR-NOC-005 (the shared `ShellSplitPanelContext` drill-down pattern this story extends) |

## Context

ADR-NOC-007's topology map composes an incident/health overlay client-side
from two independently-resolving fetches, `listIncidents` and
`getAssetHealth` (`web/src/topologyOverlay.ts`'s `buildTopologyOverlay`).
`TopologyPage.tsx`'s `selectNode` built the `SplitPanel` content for a
clicked node once, at click time:

```ts
const selectNode = (node: AssetGraphNode) => {
  setSelectedAssetId(node.asset_id);
  openSplitPanel(node.name, <TopologyNodeDetail node={node} overlay={overlayFor(overlay, node.asset_id)} />);
};
```

`Shell.tsx`'s `ShellSplitPanelContext.openSplitPanel(header, content)` stores
`content: ReactNode` as a plain value in `Shell`'s own `useState` — a
snapshot of whatever `overlay` happened to be at the moment `selectNode` ran,
not a live reference to `TopologyPage`'s ongoing state. If a user clicked a
node before `listIncidents`/`getAssetHealth` resolved, `overlay` was still
`buildTopologyOverlay([], null)` — no incidents, no health issues — and the
panel rendered exactly that: `None` for open incidents, no CMDB health
section, even for a node that in fact had an open incident. Because nothing
re-ran `openSplitPanel` when the fetches later resolved, that snapshot never
updated. Only closing and re-clicking the node (which called `selectNode`
again, rebuilding the content from the by-then-resolved `overlay`) fixed it.

This is the same shape ADR-HARD-001 documented as a **noticed-but-out-of-scope**
finding during its test-determinism audit: two independently-resolving
fetches feeding one render, with something reading them before both had
settled. ADR-HARD-001 was a test-hygiene story and explicitly deferred this
one as "a potential real production staleness bug, not a test flake" for a
dedicated story — this is that story.

`IncidentDetailPanel` (`web/src/components/IncidentDetail.tsx`), by
contrast, does not have this problem, because it is not a snapshot: it is
handed only `incidentId` and fetches its own data on mount, so it re-renders
itself when its own state updates — the split panel host's `content` never
needs to be recomputed and re-`openSplitPanel`'d by the parent for it to stay
current. `TopologyNodeDetail` is different by construction: `AssetGraphNode`
comes from the already-fetched graph (no re-fetch needed for that part), but
the overlay is derived from *parent* state (`TopologyPage`'s `overlay`
`useMemo`), which the child component has no way to subscribe to on its own
without becoming reactive by other means.

## Decision

**Chose (a): a `TopologyPage`-local `useEffect` that re-materializes and
re-`openSplitPanel`s the selected node's content when the overlay changes —
not (b), a live component subscribed to context.**

1. **A distinguishable loading state, not just "empty".** `TopologyPage`
   tracks `overlayLoading` (`useState(true)`, cleared via
   `Promise.allSettled([incidentsDone, healthDone])` once both overlay
   fetches have settled — success or failure, matching the existing
   independent-degrade behavior). `TopologyNodeDetail` takes this as an
   optional `overlayLoading` prop: while true, the "Open incidents" field
   shows `<StatusIndicator type="loading">Checking…</StatusIndicator>`
   instead of `None`, and the CMDB health section is withheld rather than
   rendered as confirmed-absent. Without this, `overlay` defaulting to empty
   before the fetches resolve is indistinguishable from a confirmed-empty
   node — exactly the ambiguity that made the original bug look like "no
   incidents" instead of "not loaded yet".
2. **A second `useEffect`, keyed on `[overlay, overlayLoading]` only,
   re-opens the panel with fresh content for the currently selected node.**
   `selectNode` still calls `openSplitPanel` directly and unconditionally on
   click (unchanged from before — a click must always (re)open the panel,
   even one the user had closed). The new effect is deliberately *not* keyed
   on `selectedAssetId`/`isSplitPanelOpen`/`graph`/`openSplitPanel` — those
   are read from the closure when the effect *does* run (which only happens
   when the overlay itself changes), not used to trigger it. This keeps
   "click opens" and "overlay-arrival refreshes" as two independent,
   non-conflicting triggers into the same `openSplitPanel` call, rather than
   one hook trying to serve both jobs (and needing to distinguish "first
   open" from "refresh" internally).
3. **The refresh effect never reopens a panel the user closed.** It guards
   on `isSplitPanelOpen`, a new, additive boolean on
   `ShellSplitPanelContext` (`web/src/Shell.tsx`) mirroring `Shell`'s own
   `splitPanelOpen` state. This was the one point where a purely
   `TopologyPage`-local fix was not possible: `Shell` owns whether the panel
   is currently visible (the user can close it via the panel's own close
   control, which calls `onSplitPanelToggle` inside `Shell`, never routed
   back to the page), and `TopologyPage` has no other way to know "is the
   panel I opened still open" without it. Adding a read-only `boolean` to the
   context is additive, not a change to the open/close contract itself — every
   other consumer (`IncidentBoardPage`, `AlertsBoardPage`, `OnCallBoardPage`,
   `EscalationBoardPage`) destructures only `openSplitPanel`
   (and sometimes `closeSplitPanel`), so the extra field changes nothing
   about how they compile or behave. This is judged the smallest change that
   makes "don't reopen a closed panel" actually true, rather than "true by
   coincidence because the fetches usually settle before anyone closes the
   panel".

### Why (a) over (b)

(b) — making `TopologyNodeDetail` a live component that reads current
overlay via props/context so it self-refreshes — was rejected because there
is no version of it that is actually less invasive than (a) once examined
concretely. `Shell` stores `content: ReactNode` as a plain value in React
state; a `ReactNode` produced by JSX (`<TopologyNodeDetail .../>`) is a
already-instantiated element describing type+props *at creation time* — it
is not a live subscription to whatever created it, regardless of whether
that creator is `TopologyPage` or something else. For `TopologyNodeDetail`
to re-render on new overlay data without a fresh `openSplitPanel` call, one
of two more invasive things would be required:

- **Give it its own subscription mechanism** (a shared store /
  `useSyncExternalStore` for the overlay, or lifting overlay state above
  `Shell` into some context every route can read) — a new piece of shared
  state infrastructure for what is currently one page's derived `useMemo`,
  disproportionate to the bug.
- **Change `Shell`'s contract so `content` is a render function
  (`() => ReactNode`) evaluated on every `Shell` render**, letting the
  closure captured at `openSplitPanel` call time read fresh values each time
  `Shell` re-renders. This *would* work, but it changes what every existing
  split-panel consumer passes to `openSplitPanel` (a function instead of a
  plain element) — exactly the kind of Shell-contract change the story's
  hard constraints ask to avoid unless clearly the cleaner path, and here it
  is not: it touches four other pages' call sites for a problem specific to
  one of them.

(a) requires no new abstraction: `openSplitPanel` keeps its existing
`(header, content)` signature, every other caller is untouched, and the fix
is legible as "the same call `selectNode` already makes, invoked again when
the thing it depends on changes."

## Alternatives considered

- **Fetch the overlay inside `TopologyNodeDetail` itself** (matching
  `IncidentDetailPanel`'s self-fetching pattern). Rejected: the overlay is
  not per-node data with its own endpoint — it is a merge over the *whole
  tenant's* incidents and health report, already fetched once by the page for
  the graph coloring itself (ADR-NOC-007 Decision 4). Re-fetching it per
  node-click would duplicate two full-tenant requests for data already in
  memory, and would reintroduce a second, node-scoped race independent of the
  page's own already-correct degrade-on-failure handling.
- **Poll or `setInterval` a re-render while the panel is open.** Rejected:
  the actual event ("overlay finished resolving") is already an exact,
  observable state transition (`overlayLoading` flipping to `false`, or
  `overlay`'s reference changing); polling would trade an exact trigger for
  an approximate, wasteful one.
- **Close the panel automatically at click time until the overlay resolves,
  then auto-open it.** Rejected: this fights the constraint that a click
  must open the panel immediately (the operator expects the detail pane to
  appear when they click, even if some of its content is still loading) — an
  explicit loading state inside the already-open panel is a better UX than a
  panel that flickers closed-then-open.

## Evidence

**Confirmed failing before the fix.** The new
`routes/TopologyPage.test.tsx` test, `refreshes the split panel once the
overlay resolves, for a node clicked before it does (ADR-HARD-002)`, holds
the mocked `/admin/incidents` fetch open behind a manually-resolved promise,
clicks a node while it is still pending, asserts the panel does **not**
already claim `1 open incident`, then resolves the fetch and asserts (via
`waitFor`, no re-click) that the panel updates to show it. Run against the
pre-fix code:

```
✗ topology map > refreshes the split panel once the overlay resolves, for a node clicked before it does (ADR-HARD-002)
  TestingLibraryElementError: Unable to find an element with the text: 1 open incident.
  (waitFor timed out — the click-time snapshot never updated)

 Test Files  1 failed (1)
      Tests  1 failed | 8 passed (9)
```

**Passing after the fix**, same test file:

```
 Test Files  1 passed (1)
      Tests  9 passed (9)
```

5 consecutive runs of `routes/TopologyPage.test.tsx` alone, all green (no
flake introduced by the new async-timing test).

**Full suite**, `make web-test`:

```
pnpm --dir web exec tsc -b --noEmit
pnpm --dir web exec vitest run
...
 Test Files  17 passed (17)
      Tests  141 passed (141)
```

**The other four `ShellSplitPanelContext` consumers, run explicitly**
(`IncidentBoardPage.test.tsx`, `AlertsBoardPage.test.tsx`,
`OnCallBoardPage.test.tsx`, `EscalationBoardPage.test.tsx` — every screen
besides Topology that calls `openSplitPanel`/`closeSplitPanel`), to confirm
the additive `isSplitPanelOpen` field on `ShellSplitPanelContext` changed
nothing about their behavior:

```
 Test Files  4 passed (4)
      Tests  43 passed (43)
```

**`make web`** — builds; `grep -o 'https\?://[^"'"'"']*'` against the built
`webdist/assets/*` returns the same inert strings ADR-UI-001/ADR-NOC-007
already accepted (SVG/XML namespace URIs, date-fns doc links, the React
error-decoder link, the Open Sans license comment) — no new external URL.
Bundle: `index-BqKJfCaf.js` 1,280.09 kB / gzip 369.21 kB (unchanged order of
magnitude from ADR-HARD-001's baseline; the pre-existing >500kB chunk
warning is E-HARD.4's concern, untouched here).

**`go build ./...`** — clean. **`make test`** — full suite green (all
packages `ok`), unaffected: no Go source, migration, or `internal/arch`
guard is touched by this story — it is `web/`-only.

**`git diff --stat`** (this story's changes only, ADR excluded from the count
below since it is itself the diff being measured):

```
 web/src/Shell.tsx                         | 14 ++++++--
 web/src/components/TopologyNodeDetail.tsx | 39 ++++++++++++++++------
 web/src/routes/TopologyPage.test.tsx      | 54 +++++++++++++++++++++++++++++++
 web/src/routes/TopologyPage.tsx           | 44 ++++++++++++++++++++++---
 4 files changed, 135 insertions(+), 16 deletions(-)
```

## Consequences

**What is now guaranteed.** Selecting a topology node whose overlay data
(open incidents / CMDB health) has not yet loaded shows an explicit
"Checking…" loading state, not a `None` that could be mistaken for a
confirmed-empty result; once the overlay resolves — with no further user
action — the same, still-open panel for the still-selected node updates to
the real overlay. Closing the panel (via its own close control) and leaving
it closed is respected: the refresh effect never reopens it. Re-clicking a
node (the same or a different one) continues to open/refresh the panel
immediately, unchanged from before. The four other `ShellSplitPanelContext`
consumers are unaffected — they don't read the new `isSplitPanelOpen` field,
and their own call sites and tests are untouched.

**What is not claimed.** This does not make `TopologyNodeDetail` a
self-fetching, fully independent component the way `IncidentDetailPanel` is
— it remains dependent on `TopologyPage` re-invoking `openSplitPanel` for
it to refresh, which is why the `[overlay, overlayLoading]`-only effect
dependency is load-bearing (get that wrong and either the refresh never
fires, or it fires on every unrelated re-render). If a future screen needs
the same "live child of a captured split-panel snapshot" pattern more than
once, revisiting `Shell`'s `content: ReactNode` contract in favor of a
render-function (option (b), rejected here as disproportionate for a single
call site) becomes the better trade — noted as a fair follow-up if that
happens, not created speculatively here.

## Enforcement

- `web/src/routes/TopologyPage.test.tsx`'s new test — the fails-before/passes-after
  proof for this exact bug and fix; a future regression that reintroduces a
  pure click-time snapshot (e.g. removing the second `useEffect`, or removing
  `overlayLoading` tracking so an unresolved overlay again renders as `None`)
  fails this test.
- `web/src/components/TopologyNodeDetail.tsx`'s `overlayLoading` prop and its
  effect on rendering — keeps "not loaded yet" visually distinct from
  "confirmed empty" so the ambiguity that made this bug hard to notice by eye
  cannot silently return.
- `web/src/Shell.tsx`'s `isSplitPanelOpen` on `ShellSplitPanelContext` — the
  four other split-panel consumers' existing test suites
  (`IncidentBoardPage.test.tsx`, `AlertsBoardPage.test.tsx`,
  `OnCallBoardPage.test.tsx`, `EscalationBoardPage.test.tsx`) continue to
  gate that this additive field never becomes a breaking one.
