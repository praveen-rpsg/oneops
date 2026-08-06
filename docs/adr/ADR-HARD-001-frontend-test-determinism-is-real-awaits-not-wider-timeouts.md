# ADR-HARD-001 — Frontend test-suite determinism: race is a bare `getBy*` after a partial await, fix is `findBy*`/`waitFor` on the actual data boundary, never a wider timeout

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | `docs/PLATFORM-BUILD-PLAN.md` E-HARD.1 (this story), ADR-UI-001 (Cloudscape + Vite console foundation — the `vitest`/`@testing-library/react` harness this ADR's fix operates inside), ADR-NOC-005/ADR-ACT-001 (the board → split-panel integration pattern the `IncidentBoardPage.test.tsx` race sits in) |

## Context

`make web-test` (140 tests / 17 files) intermittently failed — observed
roughly 4 failures across ~60 consecutive full-suite runs on this machine
(lower than the "~1-in-3" reported previously, consistent with a
machine/load-dependent race rather than a fixed ratio). Every observed
failure was the same test, `audit.test.tsx > audit timeline > reveals the
hash record on demand, not by default`, throwing
`TestingLibraryElementError: Unable to find an accessible element with the
role "button" and name /Show chain record/`.

The shape, confirmed by reading both the failing assertions and the
components they exercise, is exactly the one this story's brief predicted:

1. A test `await`s a **static** element — one that renders on first paint
   regardless of any mocked `fetch` having resolved (a `<Header>` title, an h1,
   or in the audit case, a heading that itself only needed ONE of several
   independent async fetches to resolve).
2. Having found that element, the test then makes a **synchronous**
   `getBy*`/`getAllBy*`/`queryBy*` assertion against a **different**
   element that depends on a **separate, independently-resolving** `fetch`
   promise — one the first `await` never actually waited for.
3. Under normal load the two promises settle close enough together
   (same microtask batch) that the race never shows. Under load — CPU
   contention, a slower CI runner, more test files running in parallel as the
   suite has grown — the second promise can still be in flight when the
   synchronous assertion runs, and it throws.

This is not a logic bug in the tests: every assertion fixed below still checks
exactly what it checked before. It is an **awaiting** bug — the test proved it
had waited for the wrong thing.

### The four race sites found (all in components that fetch in more than one, independently-resolving step)

1. **`AuditTimeline` (`web/src/components/AuditTimeline.tsx`)** fires two
   independent requests in its mount effect — `getTimeline` (populates
   `entries`, gates the "no operation yet" empty state, the entry list, and
   any "alert"/"Try again" error UI) and `getChainSummary` (populates `chain`,
   gates the "✓/⚠ Chain verified" badge) — while its `<Header>` ("Audit
   record") renders **unconditionally**, before either resolves. `audit.test.tsx`'s
   own `timeline()` helper `await`s that header, which is therefore not a
   useful synchronization point for anything gated on `entries`. Five
   assertions in `audit.test.tsx` used a bare `getBy*`/`getAllBy*` immediately
   after `timeline()` for content actually gated on the `entries` fetch:
   the "Show chain record" buttons, the empty-state copy, the "Ratification"
   text, the load-failure `alert`, and the "Load older" button.
2. **`IncidentDetailPanel` (`web/src/components/IncidentDetail.tsx`)** chains
   `getIncidentTimeline` inside the `.then()` of `getIncident` — a real,
   later-resolving promise relative to the incident detail itself (by design:
   "the timeline is supplementary, a failure there must not blank the
   detail"). `IncidentBoardPage.test.tsx`'s "opens an incident detail +
   timeline in the split panel on click" test correctly `waitFor`'d the
   incident description (from `getIncident`) but then asserted the timeline
   actor text (`'alert-correlator'`, from the separate `getIncidentTimeline`
   call) with a bare `getByText` immediately after.
3. **`NOCOverviewPage` (`web/src/routes/NOCOverviewPage.tsx`)** renders its
   `<Header>` ("NOC / Overview") unconditionally, before `getNOCOverview`
   resolves; the per-widget content (`IncidentsWidget`, `AssetsWidget`, etc.)
   only exists once `overview` is set. The "renders every widget cleanly at
   zero" test `await`ed only the static heading, then asserted
   `'No active on-call schedules.'` (data-dependent) with a bare `getByText`.
   (A sibling test, "renders every widget from the overview response", had
   already been fixed this same way in a prior story — the fix pattern is
   consistent, this ADR just completes it across the file.)

None of these are timing bugs that a longer `waitFor`/`findBy` timeout works
around by luck — they are missing awaits on the correct promise. The fix in
every case is to `await screen.findBy*`/`within(...).findBy*` (or wrap in
`waitFor`) the **specific** element the assertion is actually about, not to
assume an earlier, unrelated `await` already covers it.

## Decision

**Fix is `findBy*`, not a wider timeout, and only at the site of the actual
data dependency.** Concretely:

- `web/src/audit.test.tsx`: `getAllByRole('button', {name: /Show chain
  record/})` → `await ...findAllByRole(...)` before the click; the empty-state
  text, the "Ratification" text, the load-failure `alert`, and the "Load
  older" button each changed from `getBy*` to `await ...findBy*`.
- `web/src/routes/IncidentBoardPage.test.tsx`: the timeline-actor assertion
  (`'alert-correlator'`) changed from `getByText` to `await screen.findByText`.
- `web/src/routes/NOCOverviewPage.test.tsx`: the zero-state on-call copy
  (`'No active on-call schedules.'`) changed from `getByText` to
  `await screen.findByText`; everything else in that same assertion block
  stays a synchronous `getBy*`/`queryBy*`, because it is populated by the
  **same** `overview` state update as the now-awaited element — once one
  data-dependent element from a given commit is found, the rest of that same
  commit is safe to read synchronously. This is the general rule applied
  throughout: **one `findBy*` per independently-resolving async source, not
  one per assertion.**

No production code changed. No assertion was weakened, no test skipped or
deleted — every fixed test still checks exactly what it checked before; only
the wait discipline changed.

**Vitest config was not touched.** The real-await fixes fully stabilized the
suite (see Evidence); there was no remaining flake to justify touching
pool/isolation/timeout settings, and doing so would have masked the actual
defect rather than fixed it, per the brief's explicit preference.

## Alternatives considered

- **Widen `waitFor`/`findBy` default timeouts globally.** Rejected: the
  failures were not slow-render timeouts (the elements typically appear
  within single-digit milliseconds once the fix polls for them) — they were
  a missing poll entirely. A global timeout bump would reduce the failure
  *rate* without fixing the *cause*, and would slow down every failing test's
  time-to-red when a real regression does land.
- **`vitest.config` pool/isolation changes** (e.g. `pool: 'forks'`,
  `singleThread`, or reduced worker concurrency) to reduce contention.
  Rejected as the primary fix: the story brief is explicit that real awaits
  are preferred, and the 20-consecutive-green result below shows they were
  sufficient — no config change was needed to prove determinism.
- **Delete or weaken the flaky assertions.** Rejected outright — the brief's
  hard constraint, and it would silently reduce coverage of exactly the
  "reveals the hash record on demand" / "timeline renders" behaviors these
  tests exist to prove.

## Evidence

**Before (uncommitted baseline, same three files, `pnpm --dir web exec
vitest run` looped):** across roughly 60 consecutive full-suite runs on this
machine, 4 failures were observed, all the identical assertion:
`audit.test.tsx > audit timeline > reveals the hash record on demand, not by
default` — `Unable to find an accessible element with the role "button" and
name /Show chain record/`, confirming the async-race diagnosis directly (the
failing query is a synchronous `getAllByRole` racing the entries fetch).

**After (with this story's fixes applied):** 20 consecutive full-suite runs,
all green:

```
RUN 1:  Test Files  17 passed (17) / Tests  140 passed (140)
RUN 2:  Test Files  17 passed (17) / Tests  140 passed (140)
...
RUN 20: Test Files  17 passed (17) / Tests  140 passed (140)
```

(full per-run log retained in the story's working notes; every run reports
`17 passed (17)` / `140 passed (140)`, 0 failures.)

- `pnpm --dir web exec tsc -b --noEmit` — clean.
- `make web` — builds; embeds into `internal/httpapi/webdist` unchanged in
  shape (pre-existing >500kB chunk warning is E-HARD.4's concern, not this
  story's).
- `go build ./...` — clean; `make test` — green (unaffected; no Go file in
  this story's diff).
- `make lint` — `0 issues` (unaffected; no Go file in this story's diff).
- `git diff --stat` — three test files only (`audit.test.tsx`,
  `routes/IncidentBoardPage.test.tsx`, `routes/NOCOverviewPage.test.tsx`), no
  `vite.config.ts` change, no production `.tsx`/`.ts` change.

## Consequences

**What is now guaranteed.** `make web-test` is deterministic under the
conditions actually reproduced (20/20 green, up from an observed ~1-in-15
failure rate on the same machine); every fixed assertion still proves the
same behavior it proved before, just correctly synchronized to the promise it
actually depends on.

**What is not claimed.** This ADR does not claim the flake can never recur
under different load conditions than were reproduced here (a probabilistic
property can never be proven absent with a finite number of runs) — it claims
the specific root cause identified (bare `getBy*` racing an independent,
later-resolving `fetch`) is eliminated at the four sites found, and that the
audit covered every `.test.tsx`/`.test.ts` file in `web/src` for the same
shape (17 files). One further instance of the *same underlying pattern* —
`TopologyPage.tsx`'s `selectNode` capturing `overlay` (derived from two more
independently-resolving fetches, `listIncidents`/`getAssetHealth`) into a
split-panel `ReactNode` at click time, which will not refresh if the user
clicks before those fetches resolve — was noticed during this audit. It did
not reproduce as a test flake in any run performed for this story (the
existing `TopologyPage.test.tsx` assertions that depend on it are already
wrapped in `waitFor`, and in practice all three of that page's fetches settle
before the tests' `findByText` polls fire), but it is a **potential real
production staleness bug**, not a test-hygiene issue, and is out of scope for
this test-only story — reported here rather than fixed silently, per this
story's own scope boundary.

## Enforcement

- `web/src/audit.test.tsx`, `web/src/routes/IncidentBoardPage.test.tsx`,
  `web/src/routes/NOCOverviewPage.test.tsx` — the five/one/one race sites
  respectively, all converted from `getBy*`/`getAllBy*` to `findBy*`/`findAllBy*`
  at the actual data boundary.
- 20 consecutive `pnpm --dir web exec vitest run` (full suite) — all green,
  0 failures (see Evidence).
- `make web-test` remains the frontend gate (`tsc -b --noEmit` + `vitest
  run`); this ADR does not change what it runs, only makes its existing 140
  tests stop racing their own fixtures.
