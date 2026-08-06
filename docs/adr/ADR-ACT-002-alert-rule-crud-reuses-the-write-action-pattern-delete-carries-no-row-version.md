# ADR-ACT-002 — Alert-rule CRUD reuses the E-ACT.1 write-action pattern; `DELETE` carries no `row_version` because the endpoint has none, not because one was omitted

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | ADR-ACT-001 (the write-action pattern this story inherits), ADR-NOC-005 (the alerts board/detail this story turns operational), `docs/PLATFORM-BUILD-PLAN.md` E-ACT.2, `internal/httpapi/handlers_alert_rules.go` (the three write endpoints this story reuses unchanged), `internal/domain/alertrule.go` (`Validate`, `AlertRulePatch`, `AlertRuleRepository`) |

## Context

E-ACT.1 (ADR-ACT-001) turned the incident board operational and set the
pattern every later E-ACT story inherits: read once and send the
`row_version` back exactly once, confirm only consequential/destructive
moves, refetch (never trust the local mutation) on success, refetch-and-say-
so (never blind-retry) on `409`, surface every other RFC 7807 detail inline.
E-ACT.2 applies that pattern to `alert_rule`'s own three write endpoints —
create, patch, delete — reachable from the read-only alerts board/detail
ADR-NOC-005 built.

**The contract, confirmed against the Go before any UI was written**
(`internal/httpapi/handlers_alert_rules.go`, `internal/domain/alertrule.go`):

- `POST /v1/admin/alert-rules` (`createAlertRuleRequest`): `asset_id`
  (required, re-verified against the caller's tenant server-side — a
  cross-tenant or nonexistent `asset_id` is `404`), `metric` (required,
  lower-case snake_case, `domain.telemetryMetricPattern`
  `^[a-z][a-z0-9_]*$`, ≤100 chars), `comparator` (required, one of
  `gt`/`lt`/`gte`/`lte`), `threshold` (required, finite number),
  `for_duration_seconds` (required, integer 1–86400), `severity` (optional,
  defaults to `warning`; one of `critical`/`warning`/`info`), `symptom_class`
  (optional, defaults to `unspecified`; one of
  `availability`/`resource`/`unspecified` — E3.4), `enabled` (optional
  pointer, defaults to `true`), `flap_dwell_seconds` (optional pointer,
  defaults to 60; integer 0–86400). `rule_id` is minted server-side and never
  accepted from the caller. Returns `201` + the full `alertRuleDTO`, which
  carries `row_version` (confirmed at `handlers_alert_rules.go:33`).
- `PATCH /v1/admin/alert-rules/{id}` (`patchAlertRuleRequest`): **requires**
  `row_version` (rejected `< 1` as a `422`) and at least one of `comparator`,
  `threshold`, `for_duration_seconds`, `severity`, `symptom_class`,
  `enabled`, `flap_dwell_seconds` (a body with none set is also a `422`).
  **`asset_id`/`metric` are NOT patchable** — `domain.AlertRulePatch`'s own
  doc comment is explicit: what a rule watches is fixed at creation; delete
  and recreate instead. A stale `row_version` (`ErrVersionMismatch`) maps to
  `409` here — the same convention `transitionIncident`/`assignIncident` use,
  not the governance surface's `412`. Applying *any* patch — including one
  that only touches `enabled` — clears the rule's in-flight
  `pending_state`/`pending_since` (E3.2's dwell bookkeeping) as a side effect
  the console does not need to know about, only not break.
- `DELETE /v1/admin/alert-rules/{id}`: **takes no request body and no
  `row_version` at all.** `deleteAlertRule`'s handler calls
  `s.alertRules.Delete(ctx, id)` directly; `domain.AlertRuleRepository`'s
  `Delete(ctx context.Context, ruleID string) error` signature has no
  optimistic-lock parameter to pass one through even if the handler wanted
  to. This is asymmetric with `patchAlertRule` and with every E-ACT.1
  endpoint, all of which are `row_version`-guarded. Returns `204` on success,
  `404` if `ruleID` does not name a rule visible to the caller's tenant.
- **Enable/disable is not a dedicated route.** There is no
  `POST .../enable` or `.../disable`. It is `enabled` on
  `patchAlertRuleRequest`, patched exactly like any other field, under the
  same `row_version` guard as every other patch.
- `alertRuleDTO` carries `row_version` (confirmed at
  `handlers_alert_rules.go:33`) — the token `patchAlertRule`
  reads once and sends back exactly once, the same discipline
  ADR-ACT-001 established for incidents.

## Decision

### 1. Create, edit, enable/disable follow ADR-ACT-001's pattern exactly; delete cannot, and says so

Create (`POST`) and edit/enable-disable (`PATCH`) round-trip `row_version`
and refetch-on-409 identically to `transitionIncident`/`assignIncident`.
Delete cannot follow that shape — there is no `row_version` parameter on the
endpoint to send. The console does **not** fabricate one to look consistent
with the other two write actions (e.g. a client-side "are you sure the
version is still N?" check that the server would silently ignore). Instead:

- The delete confirmation `Modal` names the gap directly ("Unlike Edit, there
  is no optimistic-lock check on delete — the endpoint takes no
  `row_version`").
- `alertRules.ts`'s `deleteAlertRule` doc comment records the same fact at
  the point of use, the same way `users.ts` records the `requirePlatformAdmin`
  gap ADR-ACT-001 §2 found.

**Consequence, stated plainly:** if operator A opens a rule's detail and
operator B edits it a second later, A's subsequent Delete still succeeds
against the *current* (B-edited) row — there is no way for this endpoint to
detect or refuse that, unlike a stale-`row_version` `PATCH`. This is a
genuine, load-bearing gap in the existing HTTP contract, not a defect
introduced by this story; a correctly-scoped fix would add an optional
`row_version` query parameter or header to `DELETE` and thread it through
`AlertRuleRepository.Delete`, which is a Go change explicitly out of this
frontend-only story's scope.

### 2. One shared config sub-form, not two independent ones

`components/AlertRuleForm.tsx`'s `AlertRuleConfigFields` renders the seven
fields `patchAlertRuleRequest` accepts (comparator, threshold, for-duration,
severity, symptom-class, flap-dwell, enabled) as a presentational,
fully-controlled component. Both the create modal
(`routes/AlertsBoardPage.tsx`'s `CreateAlertRuleModal`, which additionally
collects `asset_id`/`metric` — fixed only at creation) and the edit modal
(`components/AlertRuleDetail.tsx`'s `EditAlertRuleModal`, which does not,
since `AlertRulePatch` cannot touch them) reuse it unchanged. This differs
from ADR-ACT-001 §4's choice to keep the incident create modal and the
consequential-transition confirm modal as two independent components: there,
the two dialogs' *fields* had nothing in common (a title/description/
severity/asset_id form vs. a single confirm-or-cancel prompt). Here, six of
the seven fields are byte-for-byte identical between create and edit, so
extracting them avoids maintaining two copies of the same enum-`Select`
wiring that would drift the moment one of `alertRules.ts`'s enum arrays
changes.

Every enum field (`comparator`, `severity`, `symptom_class`) renders as a
Cloudscape `Select` whose options come directly from `alertRules.ts`'s
`ALERT_COMPARATORS`/`ALERT_SEVERITIES`/`ALERT_SYMPTOM_CLASSES` — the same
arrays `listAlertRules`'s own type aliases are built from — so a value the
backend does not accept cannot be constructed through this form.

### 3. Client-side validation mirrors `domain.AlertRule.Validate`'s bounds, restated, not derived

`alertRules.ts`'s `alertRuleAssetIdError`/`alertRuleMetricError`/
`alertRuleThresholdError`/`alertRuleForDurationError`/
`alertRuleFlapDwellError` restate `Validate`'s field-level rules (metric
pattern/length, threshold finiteness, for-duration 1–86400, flap-dwell
0–86400) — the same "restate what the server would reject anyway" posture
`INCIDENT_TRANSITIONS` documents for its own reason (no runtime "what is
valid" field exists on any DTO to derive this from instead). These are
pure functions over the raw `Input` string, used to both drive inline
`errorText` and gate each modal's submit button; the server's own `422` is
still the actual source of truth (exercised directly by a dedicated test
in each modal, the same as ADR-ACT-001's create-incident validation test).

### 4. Enable/disable is a plain `Button`, not a `Toggle`, on the detail panel

The brief allowed either shape ("a toggle/button"). A `Button` labelled
"Enable"/"Disable" (whichever is the rule's *next* state) was chosen over a
`Toggle` on the detail panel itself for the same reason `IncidentDetailPanel`
uses `Button`s for its status transitions rather than a state-machine
`Select`: it reads as an action taken, not a persistent settings control, and
it is trivially disabled-while-pending the same way every other action
button here is. A `Toggle` is still used *inside* `AlertRuleConfigFields`
(create/edit) for the `enabled` field there, since that context is genuinely
"set this field as part of a form," not "take this action now." This is not
classified as consequential (no confirm `Modal`) — disabling a rule is
reversible (re-enable), the same "routine, not terminal" reasoning
ADR-ACT-001 §1 gives for acknowledge/reopen.

### 5. Delete is the one consequential action; its confirm `Modal` names the row_version gap, not just "this cannot be undone"

Unlike edit/enable-disable, Delete is gated by a confirmation `Modal`
(ADR-ACT-001 §1's rule: confirm only the consequential/irreversible moves).
Its body states both facts an operator needs: the deletion is permanent, and
the confirmed contract fact from §1 above (no optimistic-lock check exists on
this specific action). On success, the panel calls `onDeleted` rather than
the more general `onChanged` — the difference matters because the rule the
panel was showing no longer exists; `onDeleted`'s host implementation
(`AlertsBoardPage.tsx`) both closes the `SplitPanel` and reloads the board,
where `onChanged` (used by edit/enable-disable) only reloads.

## What this story explicitly does not do

- No Go, migration, or `openapi.yaml` change. `createAlertRule`/
  `patchAlertRule`/`deleteAlertRule` call the three endpoints exactly as
  `handlers_alert_rules.go` already defines them — field-for-field, verified
  before any TypeScript was written.
- No fix for the `DELETE`-has-no-`row_version` gap (§1) — a real, confirmed
  contract asymmetry, explicitly left for a follow-up Go change (an optional
  `row_version` parameter threaded through `AlertRuleRepository.Delete`)
  rather than fabricated or silently worked around here.
- No `current_incident_id` DTO field. `docs/PLATFORM-BUILD-PLAN.md`'s E-ACT.2
  line offered bundling this additive Go field (the ADR-NOC-005 "linked
  incident" column gap) into this story; the brief this story was actually
  built against was explicit that this is a **frontend-only** story and any
  missing field/route should be reported, not added. `current_incident_id`
  is not missing from what CRUD needs (create/edit/delete never touch it) —
  it remains the documented, separate follow-up ADR-NOC-005 §3 already
  names.
- No `Idempotency-Key` on any of the three calls — confirmed against
  `handlers_alert_rules.go` that none of them read that header, the same
  finding ADR-ACT-001 made for the incident endpoints.
- No bulk actions from the board (enable/disable/delete many rules at once)
  — ADR-NOC-005 scoped the board to view + drill-down; this story extends
  the drill-down to single-rule actions only.
- Does not touch `IncidentDetail.tsx`/`incidents.ts` or the incident write
  actions — this story's mutations are alert-rule-scoped, sharing only
  `api.ts`'s generic `postJSON`/`patchJSON`/`deleteJSON` helpers.

## Consequences

**What is now guaranteed.** An operator can register a new threshold rule,
change any of its patchable fields (comparator/threshold/duration/severity/
symptom-class/flap-dwell/enabled), toggle it enabled/disabled, and delete it,
all from the console. Create and edit are optimistic-lock correct: a stale
`row_version` on edit is refused server-side and surfaced via refetch, never
silently overwritten (`AlertRuleDetail.test.tsx`'s 409 test proves the patch
is sent exactly once and the panel closes its dialog, refetches, and shows a
notice rather than retrying). Every enum field is impossible to submit with
a value the backend does not accept, proven by test, not by inspection.

**What is not claimed.** Delete has no concurrent-edit protection — a real,
named gap (§1), not a silent one; an operator relying on "the version I'm
looking at is still current" for Delete the way they can for Edit is
mistaken, and the confirm modal says so in its own text. There is no bulk
action. The `current_incident_id` gap ADR-NOC-005 recorded is unchanged by
this story.

## Evidence

- `web/src/api.ts` — `patchJSON`/`deleteJSON`, the generic write helpers this
  story adds alongside E-ACT.1's `postJSON`; doc comments record why no
  `Idempotency-Key` is sent and why `deleteJSON` resolves with no value
  (every DELETE it calls returns `204`).
- `web/src/alertRules.ts` — `createAlertRule`/`patchAlertRule`/
  `deleteAlertRule` (with `deleteAlertRule`'s doc comment recording the
  no-`row_version` contract fact in full), the `ALERT_RULE_FOR_DURATION_*`/
  `ALERT_RULE_FLAP_DWELL_*`/`ALERT_RULE_METRIC_*` bounds mirroring
  `domain.AlertRule.Validate`, and the five `alertRule*Error` validators.
- `web/src/components/AlertRuleForm.tsx` — `AlertRuleConfigFields`, the
  shared seven-field sub-form.
- `web/src/components/AlertRuleDetail.tsx` — `AlertRuleDetailPanel`'s new
  action state (busy/actionProblem/conflictNotice/editOpen/confirmDelete),
  `EditAlertRuleModal`, the Edit/Enable-Disable/Delete buttons, the delete
  confirm `Modal`.
- `web/src/routes/AlertsBoardPage.tsx` — the "Create rule" `Button` +
  `CreateAlertRuleModal`, and `reload`/`onDeleted` threaded into
  `AlertRuleDetailPanel`.
- `web/src/AlertRuleDetail.test.tsx` (6 tests) — edit sends row_version +
  patched fields then refetches, asset_id/metric not editable, 409-closes-
  dialog-refetches-with-notice-no-retry, enable/disable sends row_version
  then refetches, delete confirms-then-sends-no-row_version-then-closes-
  panel-and-reloads-board, delete error surfaced without removing the rule.
- `web/src/routes/AlertsBoardPage.test.tsx` (+4 tests) — create disabled
  until required fields filled, enum Selects constrained to the real value
  sets, create posts the exact body and opens the new rule, create
  validation error keeps the dialog open.
- `pnpm --dir web exec tsc -b --noEmit` — clean.
- `pnpm --dir web exec vitest run` (`make web-test`) — 111 tests green (101
  pre-existing + 10 new), no existing assertion weakened.
- `make web` — builds cleanly (~1,209 kB JS/~350 kB gzip, ~1,177 kB CSS/
  ~239 kB gzip — a modest increase from ADR-ACT-001's own baseline); the
  same `grep -Eo 'https?://...'` sweep every prior ADR has run returns only
  the same inert strings (XML/SVG namespace URIs, a date-fns/React dev-mode
  doc link, a Google Fonts license comment over an embedded `data:` font) —
  no new runtime CDN reference.
- `go build ./...` and `make test` (full suite, `-race`) — green, unaffected;
  this story touches no Go source (`git diff --stat` shows only `web/`).
- `make lint` — 0 issues (Go-only; unaffected by construction).

## Enforcement

- `web/src/AlertRuleDetail.test.tsx` and the "create alert rule" cases in
  `web/src/routes/AlertsBoardPage.test.tsx`, under `make web-test` — this
  ADR's own claims (row_version round-trip on create/edit, no-row_version-on-
  delete, 409 discipline, enum constraint, error surfacing) checked on every
  build, not by inspection.
- Any future change to `domain.AlertRuleRepository.Delete`'s signature (e.g.
  adding an optimistic-lock parameter to close the §1 gap) should be
  mirrored into `alertRules.ts`'s `deleteAlertRule` and this ADR updated —
  there is no generated-from-Go check today, the same limitation
  ADR-ACT-001's own Enforcement section names for `INCIDENT_TRANSITIONS`.
