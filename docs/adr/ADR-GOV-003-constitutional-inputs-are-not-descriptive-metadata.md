# ADR-GOV-003 — §9.1 constitutional inputs are not descriptive metadata

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-29 |
| **Decider** | Acting CTO |
| **Related** | ADR-GOV-002 (destruction has one door), ADR-CP5/CP6 (constitutional vs descriptive ownership), ADR-AUDIT-005 (atomic constitutional mutation), CA-3 **CMR-A05** (the constitutional half of this finding, referred) |

## Context

Three metadata keys — `responsibilities`, `citations`, `coverage` — are not
descriptive data. The corpus says so plainly:

> constitutionalMetadataKeys are the metadata keys that are NOT descriptive: they
> are read by the §9.1 evaluators and therefore determine computed Authority and
> the four-part Replacement Test. […] a surface that may write arbitrary metadata
> must exclude them explicitly.

That last sentence is a convention, and the architectural law is that no
invariant may depend on developer discipline. One surface honoured it. The
writes happen somewhere else entirely, and three surfaces reached them.

**1. Creation wrote them outright.** `patchRequest.toPatch()` refused the keys;
`createRequest.toDomain()` passed `Metadata` straight through. Proven live:

| step | result |
|---|---|
| create with `metadata:{"responsibilities":"r1,r2,r3"}` | stored |
| `PATCH` the same key | **400** — "constitutional input (§9.1) and may not be set through patch" |
| ratify the artifact | `ratified` / `current_baseline` |
| replace it with a successor declaring nothing | **409** — Replacement Test refuses |
| replace it with a successor **seeded at creation** with `r1,r2,r3` | **200 — granted** |

A client decided the verdict of the four-part Replacement Test on a ratified
current-baseline artifact, using an unaudited field on an ordinary create. The
same key the platform refuses on one endpoint is accepted on another.

**2. Bulk creation, identically.**

**3. Patch destroyed them.** The patch path replaces the descriptive map
wholesale: `DELETE FROM configuration_metadata WHERE cfg_id=$1`, then re-insert.
So the guard refused to *set* a constitutional key while the same request
*erased* one. Proven live: patching an unrelated key returned **200** and removed
`responsibilities` entirely.

The third is the sharpest, because erasure is as powerful as forgery here:
`owns(new, allResponsibilities(old))` is satisfied when `old` declares nothing.
Erasing the incumbent's responsibilities makes any successor a complete
replacement.

The failure class is: **a constitutional input writable through a channel that
performs no constitutional check and writes no audit event** — and, equivalently,
destructible through it.

### The half that is not this ADR's to fix

Attacking this exposed a second, distinct defect. With **no metadata at all** —
the default for every object created without it — a ratified `current_baseline`
artifact was replaced by a bare successor with **200**:

```
OLD (no metadata) = ratified current_baseline
replace with successor declaring nothing -> HTTP 200
```

`allResponsibilities(old) = ∅`, and `owns(new, ∅)` is vacuously true, so the
clause passes on absence of evidence. The implementation is **faithful to the
ratified §9.1 text**; the vacuity is a property of the constitution, not of the
code. Changing what a ratified clause means is an Amendment, and the Amendment
Process is not open.

This ADR therefore fixes only the engineering defect — the writable/erasable
channel — and refers the constitutional defect to the register as **CMR-A05**
with the live evidence. Closing the write channel does not close the vacuous
pass, and this ADR does not claim it does.

## Decision

**The descriptive-metadata channel can neither write nor destroy a §9.1
constitutional input, and the rule is enforced where the write happens.**

1. **Enforcement moves to the storage chokepoint.** Both writers in
   `ConfigObjectRepo` — `insertTx` (create and bulk) and `Update` (patch) —
   refuse a constitutional key. Every present and future caller passes here; a
   transport-layer check protects only the callers someone remembered.

2. **A wholesale metadata clear spares them.** The patch path's delete becomes
   `DELETE … WHERE cfg_id = $1 AND key <> ALL(ConstitutionalMetadataKeys())`.
   Refusing to set a key while allowing the same request to erase it is not a
   guard.

3. **The ingress boundary refuses them too**, so callers get a clean 422 rather
   than a storage error. `ValidateInception` is the one boundary both create
   paths pass through, and it lives in the domain so every in-process caller
   observes it (ADR-CP5).

4. **One definition of the key set.** `domain.ConstitutionalMetadataKeys()` /
   `IsConstitutionalMetadataKey` remain the single source; no layer hard-codes
   the literals. A second list drifts, and a key missing from one copy is an open
   door.

## Consequences

**What is now guaranteed.** No API surface can set or destroy a §9.1
constitutional input. The Replacement Test and computed Authority operate on data
the registry cannot manipulate, so a client can no longer forge a successor that
satisfies the test, nor erase an incumbent's responsibilities to make any
successor satisfy it.

**What is *not* claimed — and this matters.**

- **The vacuous pass is untouched.** An artifact with no declared
  responsibilities can still be replaced by any successor, because §9.1 says so.
  Referred as **CMR-A05**.
- **There is now no API path to establish §9.1 inputs at all.** The platform has
  no constitutional operation for declaring responsibilities, citations or
  coverage, and this ADR removes the unconstitutional one. Until such an
  operation exists, these inputs can only enter through direct SQL. That is the
  honest state; it is stated here rather than papered over, and it is part of
  what CMR-A05 must resolve.
- **Direct database access bypasses this**, as it bypasses every application
  control. The audit-immutability triggers and the schema sentinel
  (ADR-SECURITY-002) are the controls that apply there.
- **This closes a forgery channel, not an authorisation gap.** Nothing here makes
  declaring responsibilities an audited constitutional act; it makes it
  impossible through the descriptive channel.

**Breaking change.** `POST /v1/artifacts` and `POST /v1/artifacts/bulk` now
return **422** when the body carries `responsibilities`, `citations` or
`coverage` in `metadata`. Callers doing so were deciding constitutional verdicts
with an unaudited field.

**Test-suite consequence, deliberately visible.** The authority integration tests
previously seeded these inputs through `Create`. They now seed them with direct
SQL through a helper whose comment states why. The tests were routing through the
door this ADR closes; making them use the only remaining path keeps the
constitutional gap visible in the suite rather than hidden by a convenience.

## Evidence

Live exploit, before the change:

- create with `responsibilities` → stored; `PATCH` of the same key → 400.
- successor seeded at create → Replacement of a ratified `current_baseline`
  artifact went **409 → 200**.
- `PATCH` of an unrelated descriptive key → **200**, `responsibilities` erased.
- no metadata at all → replacement of a ratified `current_baseline` artifact
  → **200** (the constitutional half; referred, not fixed).

Live re-attack, after the change:

| case | before | after |
|---|---|---|
| create with a constitutional key | stored | **422** |
| bulk create with a constitutional key | stored | **422** |
| patch of an unrelated key | erased `responsibilities` | **preserved**, descriptive key still updated |
| create a successor with crafted `responsibilities` | accepted | **refused** |
| replace with a plain successor (incumbent's inputs intact) | 200 | **409** |

Full suite green under `-race` against real PostgreSQL, all 19 packages.

## Enforcement

- `arch.TestMetadataWrites_AreGuardedAtTheStorageChokepoint` — every function
  that writes `configuration_metadata` refuses constitutional keys; fails if no
  writer is found, so the assertion cannot go vacuous.
- `arch.TestMetadataClear_PreservesConstitutionalInputs` — every wholesale clear
  excludes them.
- `arch.TestInception_RefusesConstitutionalMetadata` — the create boundary
  refuses them.
- `arch.TestConstitutionalMetadataKeys_HaveOneDefinition` — no layer hard-codes
  the literals.
- `postgres.TestConstitutionalMetadata_CannotBeWrittenOnCreate` — table-driven
  over every key.
- `postgres.TestConstitutionalMetadata_SurvivesADescriptivePatch` — the erasure
  exploit as a regression test, and the descriptive update still applies.
- `postgres.TestConstitutionalMetadata_CannotBeSetOnPatch` — storage refuses,
  independently of the transport check.

Mutation-verified: removing the storage insert guard, restoring the destructive
clear, and removing the inception check each fail the build — three architecture
diagnostics naming this ADR, plus integration failures reproducing the original
exploit (`storage accepted the §9.1 input "responsibilities"`).
