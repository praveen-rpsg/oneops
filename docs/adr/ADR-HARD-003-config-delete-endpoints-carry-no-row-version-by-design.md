# ADR-HARD-003 — Config-delete endpoints carry no `row_version` by design; the one safety-bearing case is closed at page-time, not at delete-time

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-06 |
| **Decider** | Acting CTO |
| **Related** | ADR-ACT-001 (the write-action pattern), ADR-ACT-002 §1 (first named this gap, for `alert_rule`), ADR-ONCALL-003 §4 (page-time active-membership re-check), `docs/PLATFORM-BUILD-PLAN.md` E-HARD.5, `internal/domain/{alertrule,maintenance_window,oncall,escalation}.go` (the four `Delete` signatures), `internal/escalation/worker.go` (`ActiveMember`) |

## Context

Every *update* path in the console is optimistic-lock correct: the client
reads a row's `row_version` once and sends it back exactly once, and a stale
version is refused with a `409` and surfaced via refetch, never silently
overwritten (ADR-ACT-001). The four config **delete** endpoints do not fit
that shape — none of their repository signatures has a version parameter to
send:

- `AlertRuleRepository.Delete(ctx, ruleID)` — `internal/domain/alertrule.go:363`
- `MaintenanceWindowRepository.Delete(ctx, windowID)` — `internal/domain/maintenance_window.go:148`
- `OnCall…Repository.Delete(ctx, scheduleID)` (schedule + participant) — `internal/domain/oncall.go:290`
- `EscalationRepository.Delete(ctx, policyID)` (policy + tier) — `internal/domain/escalation.go:199`

ADR-ACT-002 §1 already named this asymmetry honestly for `alert_rule` and
left the question open: close it (thread an optional `row_version` through
`Delete`) or accept it. E-HARD.5 is that decision, taken once for all four.

The concrete worry a delete-time lock would guard against: operator A opens a
config object's detail, operator B mutates it a second later, and A's delete
still succeeds against B's now-current row without A knowing it changed.

## Decision

**Accept the asymmetry as intentional across all four endpoints. Do not add
`row_version` to any config `Delete`.**

### Why this is a correctness non-issue, not deferred debt

1. **`DELETE` is idempotent on identity.** The `row_version` guard on a
   `PATCH` protects a *field-level merge* — "apply my change on top of the
   version I saw." A delete has no merge: the operator wants the row gone
   regardless of its current field values. Optimistic-lock on delete answers
   only a softer question — "the config changed under you, are you still
   sure?" — which is a confirmation-UX nicety, not an invariant. The
   destructive deletes are already gated by a confirm `Modal` (ADR-ACT-002 §5
   for alert rules; the same pattern on the others), and that modal is where
   "are you sure" belongs.

2. **The one case with real safety weight is already closed, at page-time.**
   Deleting an on-call participant could in principle page (or fail to page)
   the wrong person — but the escalation worker re-checks active tenant
   membership at the moment it pages (`internal/escalation/worker.go:246`,
   `w.membership.ActiveMember(...)`, ADR-ONCALL-003 §4): a removed or revoked
   on-call user is **never** paged, and the advance is recorded. That
   guarantee holds no matter when or how the participant row was deleted; a
   `row_version` on the delete would add nothing to it.

3. **Referential integrity is a separate mechanism.** "Delete a maintenance
   window / escalation tier that something else is mid-use of" is governed by
   the tables' foreign-key `ON DELETE` behaviour, not by optimistic locking.
   A version token on the delete would not make such a delete safe or unsafe;
   that is an FK-shape question, out of scope here and unaffected by this
   decision.

4. **Closing it costs more than it returns.** Making `DELETE` version-aware is
   either a *breaking* contract change (every client must now send a
   `row_version` it previously did not) or an *optional* parameter the server
   ignores when absent — which gives false confidence precisely to the callers
   who most need the real thing. Against that cost sits a tiny collision
   window on admin-only, low-frequency config deletes with low blast radius
   (the object is being removed either way). The trade does not clear.

### What would reopen this

A concrete, observed lost-work incident — an operator deleting a config object
whose meaningful change they did not see, with real consequence — would
reopen it. The fix at that point is a deliberate one: add an optional
`row_version` (header or query) threaded through the specific `Delete` that
was bitten, plus a `409` on mismatch, mirrored into that endpoint's client.
Absent such evidence, the platform does not carry a version token it does not
need.

## Consequences

**Guaranteed.** No config delete can page a revoked user or violate an FK; the
destructive deletes confirm before acting; the update paths remain
optimistic-lock correct. The contract is honest — the alert-rule delete
confirm modal and `deleteAlertRule`'s doc comment already state in their own
text that there is no optimistic-lock check on delete (ADR-ACT-002 §1, §5).

**Not claimed.** Two operators racing a config edit against a config delete is
not detected; the delete wins against the current row. This is a named,
documented property, not a silent one, and — per the reasoning above — not a
correctness defect.

## Enforcement

- The existing per-endpoint frontend tests under `make web-test` already
  assert delete sends no `row_version` and confirms first (ADR-ACT-002's
  `AlertRuleDetail.test.tsx` delete case is the template the others follow).
- If any `…Repository.Delete` signature ever gains a version parameter, that
  is the signal this decision has been deliberately reversed for that
  endpoint; this ADR must be superseded, not silently contradicted, and the
  matching client `delete…` call updated to send it.
- `internal/escalation/worker.go`'s `ActiveMember` page-time check is the
  load-bearing guarantee behind reason (2); it is covered by the escalation
  worker's own tests (ADR-ONCALL-003) and must not be removed on the
  assumption that delete-time validation replaces it — it does not.
