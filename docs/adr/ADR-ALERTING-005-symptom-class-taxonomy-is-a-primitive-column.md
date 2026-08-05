# ADR-ALERTING-005 — Alert-rule symptom-class is an explicit, default-unspecified column: the primitive only

- **Status:** Accepted
- **Date:** 2026-08-05
- **Story:** E3.4 (founder-approved + design-locked 2026-08-05; sequenced right
  after the split E3.3a/E3.3b and E4.1/E4.2, before E5.2)
- **Supersedes / amends:** none. Composes with, and is named as the future
  refinement point of, ADR-ALERTING-003 (dependency-aware suppression's
  Honest Bound) and ADR-ALERTING-004 (topology-aware grouping's Honest
  Bound), which are the two consumers this primitive exists for. Also
  composes with ADR-ALERTING-001 (flap dwell) and ADR-ALERTING-002
  (maintenance windows) purely by not touching them.

## Context

ADR-ALERTING-003 §"Honest Bound" and ADR-ALERTING-004 §"Honest Bound" both
name the same missing fact, independently: `AlertRule` records *how severe* a
condition is (`Severity`) but nothing that classifies *what kind* of
condition it is. Dependency suppression is therefore all-or-nothing per
asset — suppressing *any* firing on `X` when *any* dependency of `X` is down,
even a genuinely independent failure on `X` (its disk fills while its
database dependency happens to be down). Grouping has the analogous gap: it
cannot distinguish a truly independent coincident failure from real
collateral damage.

Both ADRs deferred the fix rather than bolting it on, because it is a real
modeling decision in its own right: what the classes are, whether a rule is
classified explicitly or inferred, and what a classified rule then does
differently. E3.4 answers the first two questions and deliberately leaves the
third to future stories — see "Primitive only" below.

## Decision

**1. The enum is minimal, not exhaustive: `availability | resource |
unspecified`.** This is the one distinction the two named consumers actually
need — does the symptom cascade through dependencies?

- `availability` — reachability/up-ness. A CI being unreachable is exactly
  the kind of condition that legitimately cascades: a downstream CI looks
  down because the CI it depends on is down.
- `resource` — a resource/utilization metric (cpu, disk, memory, latency,
  queue depth, …). Usually independent of a dependency's own health: `X`'s
  disk can fill for reasons that have nothing to do with whether `Y`, which
  `X` depends on, is up.
- `unspecified` — not classified. The default, and the only class an
  unclassified pre-E3.4 rule can have.

A richer taxonomy (per-resource-type classes, latency vs. saturation, etc.)
was considered and rejected for now: nothing in the backlog needs it yet, and
the reduced-concept discipline (§4) argues against building classification
detail ahead of a concrete consumer. Widening the enum later is an additive
migration (a new `CHECK` value), not a breaking one.

**2. Explicit, operator-set; NEVER inferred from `Metric`.** A rule does not
compute its own class from its metric name. Name-based inference (e.g.
"metric contains `up` or `reachable` ⇒ availability") was considered and
rejected: metric naming is free text (`Metric`'s own doc comment: lower-case
snake_case, not a controlled vocabulary), so a heuristic would silently
misclassify an oddly-named metric, and a silent misclassification is worse
than an honest "not classified" — it would make dependency suppression or
root-cause ranking confidently wrong instead of visibly inert. This is the
same reasoning ADR-ALERTING-003 §6 applies to suppression itself: never a
silent drop.

**3. Default is `unspecified`, not a guess — and the migration backfills
every existing row to it.** This was the one open call the founder left to
engineering ("follow best"): default to `unspecified` rather than trying to
seed real classes from existing data. `unspecified` is deliberately excluded
from any future dependency/root-cause behaviour those consumer stories add
(both name this in their own Honest Bound sections), so every rule that
predates E3.4 — and every rule created without an explicit class — keeps
behaving **exactly** as it did before this column existed. This is the load-
bearing non-breaking property of the whole story: E3.4 changes what can be
*recorded*, never what is currently *decided*.

**4. It is a column on `alert_rule`, not a reified entity.** No
`SymptomClass`/`Taxonomy` table, no join — the same reduced-concept
discipline every prior alerting ADR in this series follows (§4): `Severity`,
`FlapDwellSeconds`, `PendingState`, `CurrentIncidentID` and `RootIncidentID`
are all columns on the row they describe, never their own noun.

**5. Modeled exactly like `Severity` + `FlapDwellSeconds`, not a new
pattern.** `AlertSymptomClass` is a typed string enum with a `Valid()` method
(`Severity`'s shape); it validates in `AlertRule.Validate()` (defense in
depth, mirrored by a DB `CHECK`) and defaults inside `NewAlertRule` the same
way `FlapDwellSeconds` defaults to `DefaultAlertRuleFlapDwellSeconds` without
being a constructor parameter — adding a constructor parameter would have
forced every one of `NewAlertRule`'s existing call sites (production and
test) to change for a field every one of them wants defaulted anyway. It is
patchable via `AlertRulePatch.SymptomClass` (a `*AlertSymptomClass`, nil
meaning "leave unchanged"), the same pointer discipline `Severity`/
`FlapDwellSeconds` already use.

**6. Additive, non-breaking contract.** `symptom_class` is optional on
`CreateAlertRuleRequest` (empty string ⇒ default, mirroring `Severity`'s own
handler-level default-when-omitted), an optional pointer on
`PatchAlertRuleRequest`, and always present (never `omitempty`) on the read
DTO — every rule has one. `openapi.yaml` updated accordingly;
`make contract-breaking` against `origin/master` is clean (additive only, no
removed/narrowed field).

## Primitive only — explicitly NOT built here

E3.4 stores and exposes the field. **Nothing reads `symptom_class` to make a
decision yet.** In particular, this story does not touch:

- `internal/alerting/evaluator.go`'s suppression/dwell/correlation/notify
  logic, or `internal/grouping`'s reconciliation pass.
- `dependency_suppression`'s all-or-nothing-per-asset behaviour
  (ADR-ALERTING-003's Honest Bound stands, unchanged, until its own story).
- `incident.root_incident_id`'s single-root-pick heuristic
  (ADR-ALERTING-004's Honest Bound likewise stands, unchanged).

Two follow-on stories, both already named in the ADRs this one closes the
gap for, are expected to consume this primitive:

- **Class-scoped E3.3b dependency suppression** — suppress `X`'s firing only
  when the firing is `availability`-class and the down dependency is itself
  `availability`-class, leaving a `resource`-class firing on `X` unsuppressed
  even while a dependency is down.
- **Class-aware E4.2 root-cause ranking** — prefer an `availability`-class
  root over a `resource`-class one (or exclude `resource`-class incidents
  from the walk entirely) when picking which node "wins" the group.

Both are separate, later stories. Building either now would have expanded
this story's scope past one coherent primitive and coupled a storage decision
to a behavioural one before either consumer's own design was reviewed.

## Consequences

**Guaranteed.** Every alert rule has exactly one of `availability`,
`resource`, `unspecified`, enforced at two independent layers (domain
`Validate`, DB `CHECK ck_alert_rule_symptom_class`); every rule that existed
before this migration reads back `unspecified` and behaves identically to
before; create/patch/read round-trip through the HTTP contract; the entire
pre-existing alerting/suppression/grouping test suite passes unmodified,
proving no behavior moved.

**Not claimed.** No suppression or grouping decision is scoped by class yet
(see "Primitive only"). No richer taxonomy than the two-class + unspecified
split. No automatic reclassification of existing rules — an operator who
wants `availability`/`resource` recorded for a pre-existing rule must PATCH
it explicitly.

## Evidence

- **Domain unit (mutation-verified):** `internal/domain/alertrule_test.go` —
  enum validity (including near-miss casing/pluralization), the default
  wired through `NewAlertRule`, every defined class validating cleanly, the
  patch pointer's nil-means-unchanged discipline. Defeating the
  `!r.SymptomClass.Valid()` guard in `AlertRule.Validate` flips
  `TestAlertRule_ValidateRejectsUnknownSymptomClass` to FAIL (verified by
  hand, reverted).
- **Store integration (real PostgreSQL):**
  `internal/store/postgres/alert_rule_store_integration_test.go` — create
  with each class + with the field omitted (⇒ `unspecified`), read-back,
  PATCH under optimistic locking, a raw insert simulating a pre-migration row
  (⇒ reads back `unspecified`), and a raw insert naming an out-of-enum value
  rejected by `ck_alert_rule_symptom_class` (SQLSTATE `23514`) at the DB
  layer — the second, independent line of defense the domain check does not
  make redundant (a caller that bypasses `Validate` entirely still cannot
  write a bad value).
- **No-behavior-change proof:** the full pre-existing alerting, grouping,
  dependency-suppression and maintenance-window suites
  (`internal/alerting/...`, `internal/grouping/...`,
  `internal/store/postgres/dependency_suppression_store_integration_test.go`,
  `.../maintenance_window_store_integration_test.go`) pass unmodified — none
  of those files were touched by this story.
- Full unit suite (race): 1145 `--- PASS`, 0 `--- FAIL`. Full integration
  suite (race, real Postgres): 1518 `--- PASS`, 0 `--- FAIL`, 3 pre-existing
  unrelated `--- SKIP` (a documented mutation-by-hand test and two
  race-detector-excluded performance acceptance tests). PKG corpus census
  (`internal/kg/extract/schema`) updated for the new column + `CHECK`
  constraint (`kindColumn` 356→357, `kindConstraint` 119→120) and green.
  Both privileged arch guards
  (`TestPrivilegedReads_AreScopedToATenant`,
  `TestPrivilegedMutations_AreScopedToAnOwner`) green — this story adds no
  new privileged read/write shape. `make lint`, `make migrate-hash`,
  `make migrate-validate`, and `make contract-breaking` (against
  `origin/master`) all clean.
