# ADR-CP5: The Registry owns persistence, never constitutional mutation

- **Status:** Accepted
- **Date:** 2026-07-23
- **Constitutional authority:** State Model §8 (*"No dimension changes except as an operation specifies"*), §6 (Authority is computed), INT-3 (inception state)
- **Binding reviews:** CI-1, CI-2, CI-3, CI-4, RFC-AUTH
- **Supersedes:** the open question carried by CP-1, CP-1.2 and CP-1.3
- **Evidence:** CP-0, CP-0.1, CP-0.2

## Context

CP-1.2 and CP-1.3 hardened the HTTP surface: Create yields only the INT-3 inception state, and PATCH can no longer carry Lifecycle, Retention, Authority, or the three §9.1 metadata inputs. The **repository contract beneath them was not narrowed**, so `domain.Patch` still exposes all three dimensions and `ConfigObjectRepo.Update` still builds SQL for them.

**Measured:** no production caller sets those fields. The only remaining references are the SQL builder that consumes them — now unreachable in production — the test fake that mirrors it, and one integration test.

The architecture already separates the two persistence paths:

| Owner | Method | Transaction | Audit |
|---|---|---|---|
| Registry | `Update(ctx, cfgID, expected, *Patch)` | none | none |
| Governance | `ApplyDimensions(ctx, tx, cfgID, expected, lifecycle, retention, authority)` | engine's own | atomic |

Governance has never used `Patch`. The registry contract is simply broader than its owner's remit.

## Questions Answered

**Q1 — Should repository contracts continue exposing constitutional fields no supported caller may legally modify?**

**No.** The evidence is stronger than "unused": the SQL branches are **unreachable in production**. A contract that offers an unaudited dimension write is an attractive nuisance — the next caller to need one will find it available, compiling, and tested.

**Q2 — Where is constitutional ownership located?**

**Governance.** Three responsibilities, three owners, no overlap:

| Concern | Owner | Authority |
|---|---|---|
| Constitutional **mutation** | `governance.Execute` | §8 |
| Constitutional **computation** | Authority engine | §9.1, CI-3 Issue 3, CI-4 |
| **Persistence** | Repository | none |

**Q3 — Retain the fields for future callers, or remove them?**

**Remove entirely.** CP-0.1 measured that *multiple owners of one constitutional state* is the root cause of the whole defect class. Retaining the fields for a hypothetical caller re-creates that condition deliberately. A future caller needing a governed mutation has a path: `governance.Execute`.

**Q4 — Does repository compatibility justify retaining unused constitutional parameters?**

**No.** There is no compatibility to preserve. The SDK never calls `/v1/artifacts`; no production code constructs a `Patch` with a dimension. Compatibility would be with a caller that does not exist.

## Options

| | Option A — persistence-only | Option B — retain for future callers | Option C — alternative |
|---|---|---|---|
| Constitutional | Contract matches ownership | Contract contradicts §8 | — |
| Risk | Removes the last in-process bypass | Preserves it | — |
| Compatibility | Nothing to break (Q4) | Protects a hypothetical | — |
| Complexity | Lower — fewer fields, fewer SQL branches | Unchanged | — |

**Option C considered and rejected:** routing registry writes *through* `governance.Execute` would make every metadata edit a §8 operation requiring an audit event and an operation name §8 does not define. §8 governs dimensions, not descriptive data. The registry has a legitimate persistence role; it should keep it and nothing more.

## Decision

**Option A. Repository contracts become persistence-only.**

1. `domain.Patch` loses `Lifecycle`, `RetentionClass` and `Authority`. `ConfigObjectRepo.Update` loses the corresponding SQL branches.
2. `governance.Store.ApplyDimensions` is **unchanged** — it is governance's persistence primitive and the sole path by which a dimension reaches storage.
3. **The INT-3 inception rule moves from transport to `domain`.** CP-1.2 placed `validateInception()` in `internal/httpapi/dto.go`, which leaves a constitutional rule owned by the transport layer and unavailable to any other caller. `ConfigObjectRepository.Create` accepts a whole `ConfigObject`, so its dimensions cannot be removed — the entity *is* its dimensions. The constraint on **which state may be created** therefore belongs in the domain, where every caller is bound by it.

**On the registry HTTP surface:** CP-1 framed this ADR as possibly *removing* it. That question has been answered in practice — CP-1.2 and CP-1.3 hardened it instead, and it now writes only non-constitutional data. **The surface survives; its contract narrows.**

## Migration Impact

**No schema change. No API change** — the HTTP surface was already hardened. **No SDK change** — it never used these paths.

Compile-time breakage is the migration, and it is confined to three in-repo sites: the SQL builder in `configobject_repo.go`, the fake in `handlers_test.go`, and one assertion in `repo_integration_test.go`. Removing struct fields makes the compiler enumerate every remaining caller — the same property that made CP-1.2's blast radius exactly one file.

## Implementation Work Packages

| ID | Objective | Depends on |
|---|---|---|
| **CP-5.1** | Remove the three dimensions from `domain.Patch` and their SQL branches | none |
| **CP-5.2** | Relocate the INT-3 inception rule from `httpapi` to `domain` | none |

Both are small, independently releasable, and free of governance and RFC gates.

## Relationship to CP-1.4

**This decision changes CP-1.4's shape.** CP-1.4 was scoped as *"registry DELETE enforces §8 preconditions identically to the governed path."* Under this ADR that is the wrong remedy: teaching the registry to enforce §8 would make it a second constitutional authority — precisely the condition CP-0.2 measured as the defect.

Deletion **is** a §8 operation. So registry DELETE must either be removed, or delegate to `governance.Execute`. **CP-1.4 should be re-scoped before it is implemented**, and it remains the last unresolved registry surface.

The existing role guard in `ConfigObjectRepo.Delete` is not disturbed. §8's role prohibition is absolute and unconditional — it requires no §8 evaluation, so enforcing it at the persistence boundary duplicates no governance logic. That guard stays regardless of CP-1.4's outcome.

## Relationship to RFC-AUTH

**Confirms and constrains it.** RFC-AUTH requires the stored `authority` column to have exactly one writer. This ADR names who it is **not**: the registry. WP-A1 (remove Authority from request DTOs) is already delivered by CP-1.2 and CP-1.3; removing it from `domain.Patch` completes it at the contract level.

**WP-A2 remains blocked** on CI-2's §8 Approval derivation gap. This ADR does not unblock it.

## Consequences

**Easier.** Exactly one answer to *who owns constitutional mutation*. The compiler enforces it: with the fields gone, an unaudited dimension write cannot be written, not merely discouraged.

**Harder.** A future legitimate need for a bulk or administrative dimension change must go through `governance.Execute` — with an operation name, preconditions and an audit event. That is the intended cost.

**Residual, recorded not resolved.** `ConfigObjectRepository.Create` still accepts a fully-populated `ConfigObject`, so an in-process caller could construct a non-inception object directly. CP-5.2 mitigates this by putting the rule in the domain, but the entity remains constructible in memory. Closing that fully would require a constructor discipline the repository cannot enforce, and it is out of scope here.

## Reversal

`git revert`. The fields are additive to restore, no data is affected, and no migration is involved. Reversal would reinstate the in-process bypass, which is the reason for the change.
