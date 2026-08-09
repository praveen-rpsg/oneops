# ADR-ALERTING-006 — Dependency suppression is symptom-class-scoped: a resource symptom is never masked by an upstream outage

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-08 |
| **Decider** | Acting CTO |
| **Related** | ADR-ALERTING-003 (E3.3b dependency suppression, whose "Honest Bound" this closes), ADR-ALERTING-005 (E3.4 `alert_rule.symptom_class` primitive this consumes), `internal/alerting/evaluator.go` (`dependencySuppressed`), `internal/domain/alertrule.go` (`AlertSymptomClass`), `docs/PLATFORM-BUILD-PLAN.md` E3.5. First consumer of the E3.4 primitive. |

## Context

E3.3b suppresses a rule's `ok→firing` side-effect when the rule's asset
transitively depends on an asset that is itself down (has its own firing rule) —
so a `primary-db` outage doesn't also page for the six services that depend on
it. But it was **all-or-nothing per asset**: it suppressed X's *entire* firing
whenever any dependency was down, regardless of what X's alert was actually
about. ADR-ALERTING-003 recorded this as a deliberate Honest Bound, deferred
until a symptom taxonomy existed. E3.4 (ADR-ALERTING-005) added that taxonomy
(`alert_rule.symptom_class` ∈ `availability | resource | unspecified`) as a
primitive with no consumer yet. E3.5 is the first consumer.

## Decision

`dependencySuppressed` gates on the rule's `symptom_class`:

- **`resource`** → **never dependency-suppressed.** A CPU/memory/disk symptom on
  X is not *caused* by an upstream dependency being down; suppressing it would
  mask a genuine, independent failure. The evaluator early-returns
  `(false, nil)` before the down-check even runs (so no suppression is checked
  or recorded for a resource symptom).
- **`availability`** → suppressed when a dependency is down (the symptom
  cascades — X is unreachable *because* its dependency is). Unchanged from E3.3b.
- **`unspecified`** (the default for unclassified rules) → suppressed
  (conservative; unchanged from E3.3b, exactly as E3.4 specified the default
  behaves as it does today).

The only behavior change is that `resource`-class rules now fire through a down
dependency instead of being masked. Availability and unspecified are
byte-for-byte unchanged.

## Consequences

**Guaranteed.** An operator who classifies a rule `resource` gets paged for a
real resource problem on X even during an unrelated upstream outage — the E3.3b
masking bound no longer applies to it. Availability/unspecified keep the
cascade-suppression that prevents alert storms. The change is a single gate; the
down-check, the `dependency_suppression` store/ledger, maintenance suppression
(E3.3a), and the grouping reconciler (E4.2) are untouched.

**Residual bound (narrowed, not eliminated).** An `availability`- or
`unspecified`-class symptom that is coincidentally concurrent with, but truly
independent of, a real outage can still be masked — the same self-clearing,
visibly-recorded mitigation E3.3b already provides applies. Operators tighten
this by classifying such rules `resource`.

**Not built here.** Class-aware root-cause *grouping* (E4.2) — an incident does
not carry its rule's symptom_class, so making grouping class-aware needs that
threaded to the incident or a rule-join first; it is a separate story.

## Enforcement

- `internal/alerting/dependency_suppression_test.go`:
  `TestEvaluator_ResourceSymptomNeverDependencySuppressed` (fires + no store
  call), `..._AvailabilitySymptomStillDependencySuppressed`,
  `..._UnspecifiedSymptomStillDependencySuppressed`. Mutation-verified: removing
  the gate fails the resource test on all its assertions.
- The gate must use `domain.AlertSymptomClassResource` (not a literal); it must
  not change availability/unspecified behavior.
