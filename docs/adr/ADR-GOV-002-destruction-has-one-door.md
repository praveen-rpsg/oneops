# ADR-GOV-002 — Destroying a governed object has exactly one door

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-29 |
| **Decider** | Acting CTO |
| **Related** | ADR-AUDIT-005 (atomic constitutional mutation), ADR-AUDIT-003 (tamper-evident append-only audit), ADR-GOV-001 (extension records its edge in the constitutional transaction), ADR-CP6 (descriptive lifecycle ownership) |

## Context

The Governance Engine is the sole owner of §8 semantics. The transport layer's
own comments say so — *"the engine remains the sole owner of all business logic;
the API is transport only."* For destruction, that was not true.

Two routes destroyed a Configuration Object:

- `DELETE /v1/governance/{id}` → the engine: load-for-update, authorize, plan the
  §8 transition, check dependents, apply, append the audit event, one atomic
  commit.
- `DELETE /v1/artifacts/{id}` → `ConfigObjectRepo.Delete`, which was:

```sql
DELETE FROM configuration_object WHERE cfg_id = $1 AND role <> ALL($2::text[])
```

That second door enforced the protected-role rule and **nothing else**. It
skipped the working-material-only rule, skipped the dependents check, took no
row lock that governance operations would contend on, and — decisively — **wrote
no audit event**.

This was found by attacking a different hypothesis. A concurrency probe raced a
`DELETE` against an `extend` on the same object; both succeeded, which should be
impossible if both went through the engine's `GetForUpdate`. Following that
thread showed the two paths never contended because only one of them was the
engine.

Proven live against the running service, with no concurrency required:

| step | result |
|---|---|
| create `reference` object, ratify it | `ratified` / `current_baseline` |
| audit events on its chain | 1 |
| `DELETE /v1/governance/{id}` | **409** — the engine refuses (§8: only working_material may be deleted) |
| `DELETE /v1/artifacts/{id}` | **204** — destroyed |
| object rows remaining | 0 |
| `operation='removal'` / `'deletion'` audit events | **0** |

An object in the strongest constitutional state — ratified, in the current
baseline — was erased through the registry route with no record that it had ever
existed, while the constitutional route refused the same request seconds earlier.

The race added a second consequence. `dependency_edge` has
`ON DELETE CASCADE` on both endpoints, so the unguarded delete silently removed
the edges of an audited `extension`. The result was an immutable audit event
asserting a successor relationship that the graph no longer contained, and an
orphaned audit chain for a `cfg_id` with no object. (The orphan does not prevent
startup — the ownership validator was run against the resulting database and the
platform booted normally — so this is corruption, not a self-inflicted outage.)

Four roles are outside the protected set — `eng_spec`, `planning`, `reference`,
`working` — i.e. ordinary governed content, so the exposure was the normal case
rather than an edge case. Any caller with `delete` permission could destroy it
untraceably.

The history matters. An earlier fix had already *found* this door and hardened
it, adding the protected-role predicate to the raw SQL. Its test still carried
the comment *"The registry DELETE path does not go through the governance
engine, so it enforces §8's role prohibition itself."* The second door was known
and patched rather than removed — which is precisely the symptom fix this
programme forbids, and it left three of the four §8 preconditions unenforced.

The failure class is: **a destructive constitutional effect reachable through a
path that is not the constitutional engine.**

## Decision

**Destroying a Configuration Object has exactly one implementation, in the
Governance Engine, and the unguarded path is removed from the type system rather
than hardened.**

1. **`DELETE /v1/artifacts/{id}` executes the constitutional deletion.** The
   handler is now `s.execGovernance(w, r, domain.OpDeletion)` — byte-identical in
   behaviour to `DELETE /v1/governance/{id}`. Both routes, one operation, one set
   of rules.

2. **`ConfigObjectRepo.Delete` is deleted, not fixed.** A hardened destructive
   method is still a destructive method waiting to be wired to a new handler by
   someone who has not read this ADR. The engine reaches storage through
   `GovernanceStore.RemoveObject` inside the constitutional transaction, so
   nothing needed it.

3. **`Delete` is removed from `domain.ConfigObjectRepository`.** The persistence
   contract no longer offers destruction at all. This is the load-bearing part of
   the decision: the class cannot return through a new caller because there is
   nothing to call.

4. **Destruction without a Governance Engine is refused.** A server built without
   an engine now refuses `DELETE` rather than performing it. Failing closed on
   destruction when the authority that governs it is absent is the only
   defensible behaviour.

## Consequences

**What is now guaranteed.** Every destruction of a Configuration Object passes
the §8 preconditions (role protection, working-material-only, no dependents) and
appends exactly one audit event in the same transaction as the removal
(ADR-AUDIT-005). The audit log is therefore a complete record of object
destruction: nothing governed can cease to exist without a record of who did it
and when.

**Breaking API change, deliberately.** `DELETE /v1/artifacts/{id}`:

- now **requires** an `Idempotency-Key` header (400 without it), as every
  state-changing constitutional operation does — the audit event needs an
  operation id;
- returns **200** with the governance result instead of **204**;
- returns **409** where it previously returned 204, for any object that is not
  working material or that has dependents.

Callers that relied on unconditional deletion will now receive 409. That is the
point: those calls were destroying governed content outside the constitution.
The OpenAPI document records the change on the operation.

**What is *not* claimed.**

- **This does not make destruction reversible.** A permitted deletion still
  removes the object; what changed is that it is authorised and recorded. The
  audit chain survives the object, by design.
- **The `ON DELETE CASCADE` on `dependency_edge` is unchanged.** It remains the
  mechanism that removes a destroyed object's edges. It is safe now only because
  the dependents check runs first on every path; it is not itself a guard, and a
  future path that bypasses the engine would make it dangerous again. The
  architecture tests exist to prevent that path.
- **Database-level destruction is out of scope.** An operator with SQL access can
  always `DELETE FROM configuration_object`. That is the same residual as
  ADR-SECURITY-002, and the audit-immutability triggers and schema sentinel are
  the controls that apply there.
- **Orphaned audit chains remain possible** for objects deleted before this
  change. They are harmless to startup (verified live) and are preserved
  deliberately — the audit chain outliving its object is the intended design.

## Evidence

Live exploit, before the change: a ratified `current_baseline` object refused by
`DELETE /v1/governance/{id}` (409) was destroyed by `DELETE /v1/artifacts/{id}`
(204), leaving 0 rows and 0 deletion audit events. In the concurrent variant, a
`DELETE` and an `extend` both returned success; the object vanished, its
dependency edge was cascaded away, and the `extension` audit event survived
asserting a relationship that no longer existed.

Live re-attack, after the change, against the rebuilt binary:

| case | before | after |
|---|---|---|
| registry `DELETE` on ratified object | 204, destroyed | **409, survives** |
| registry `DELETE` on working-material draft | 204, **0** audit events | **200, 1 deletion audit event** |
| registry `DELETE` with a dependent | 204, edge cascaded away | **409, object and edge intact** |
| `DELETE` racing `extend` | both succeed, graph/audit diverge | victim survives, graph consistent |

Full suite green under `-race` against real PostgreSQL.

## Enforcement

- `arch.TestConfigObjectRepository_ExposesNoDestructiveMethod` — parses the
  interface and fails if it declares `Delete`/`Remove`/`Destroy`/`Purge`/etc.
- `arch.TestConfigObjectRepo_HasNoUnguardedDelete` — the storage implementation
  defines no `Delete` method.
- `arch.TestHTTPHandlers_DoNotDestroyObjectsDirectly` — no handler calls a
  destructive repository method, and `deleteArtifact` delegates to
  `domain.OpDeletion`.
- `httpapi.TestDestruction_RegistryRouteRefusesGovernedObject` — the live exploit
  as a regression test.
- `httpapi.TestDestruction_PermittedDeletionIsAudited` — a permitted deletion
  writes exactly one `deletion` audit event.
- `httpapi.TestDestruction_RegistryRouteHonoursTheDependentsGuard` — the
  dependents guard holds on this route, and the audited Extension's edge
  survives.
- `httpapi.TestCreateGetDelete` — without an engine, destruction is refused and
  the object survives.

Mutation-verified: restoring the pre-fix handler, the repository method and the
interface method reproduces the original exploit exactly — the architecture tests
emit six diagnostics naming this ADR, and the integration tests fail with
`GOVERNANCE BYPASS: … (status 204)` and `UNAUDITED DESTRUCTION: 0 deletion audit
events`.
