# ADR-TENANCY-010 — A drill never targets the live database, and scripts are registered tooling

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-07-29 |
| **Decider** | Acting CTO |
| **Related** | ADR-TENANCY-008 (operational tooling is in scope — **class reopened by this ADR**), ADR-SECURITY-003 (platform invariants), **EVR-006** |

## Context

From the Trust Register audit recorded in EVR-006. ADR-TENANCY-008 decided that
operational tooling is production code held to the trust model, and reasoned
explicitly that a security decision must never be *"encoded in a script"*. It
inventoried `db-backup.sh`, `db-restore.sh` and `dr-drill.sh` in its own context
table.

Its enforcement — `TestOperationalBinariesAreRegistered` — walks only `cmd/`.
Every script was therefore outside the guard, and one of them was defective.

`dr-drill.sh` derives its target from an operator-supplied variable and drops it:

```sh
DRILL_DB="${DR_DRILL_DB:-oneops_drdrill}"
...
psql "$ADMIN_URL" -c "DROP DATABASE IF EXISTS $DRILL_DB" -c "CREATE DATABASE $DRILL_DB"
```

Nothing compared `DRILL_DB` to the live database. Proven by deriving the script's
own variables against the live DSN:

```
DR_DRILL_DB=oneops_drdrill -> DROP DATABASE IF EXISTS oneops_drdrill  (live db: oneops)  safe
DR_DRILL_DB=oneops         -> DROP DATABASE IF EXISTS oneops          (live db: oneops)  <<< DROPS PRODUCTION
guard expressions in the script: 0
```

The mistake is easy to make — the live database is `oneops` and the drill default
is `oneops_drdrill` — and impossible to undo. A disaster-recovery drill that
destroys the thing it exists to prove recoverable is the worst possible failure
mode for that tool.

## Decision

1. **The drill refuses when its target is the live database.** `dr-drill.sh`
   extracts the database name from `ONEOPS_DB_URL` and exits non-zero before
   touching anything if `DR_DRILL_DB` matches it. Refuse, not warn.

2. **Scripts are registered tooling, exactly as binaries are.**
   `registeredScripts` mirrors `registeredBinaries`: every `scripts/*.sh` must be
   registered with what makes it safe, and a registration for a script that no
   longer exists also fails, so the registry cannot drift from the tree.

3. **Any script that drops a database must carry the live-database refusal**,
   pinned by its own guard so this specific failure cannot regress.

## Consequences

**What is now guaranteed.** No operational script exists outside the registry,
and no script can drop the database named in `ONEOPS_DB_URL`.

**What is *not* claimed.**

- The registry records *that* a reason was written, not that the reason is true —
  the same residual as every justification registry in this programme.
- The refusal compares database *names*. A drill pointed at a different host
  carrying a production database of another name is not detected; the guard
  addresses the mistake that was actually reachable from the documented workflow.
- `db-restore.sh` still restores over whatever target it is given, by design. Its
  safety rests on the platform invariants catching an inconsistent restore at
  startup and continuously (ADR-SECURITY-003).

## Evidence

Before: 0 guard expressions; `DR_DRILL_DB=oneops` would drop production.
After: exit code 1 with a named refusal; the live database is untouched; a
genuine throwaway target still proceeds to *"1/5 capture source state"*.

## Enforcement and mutation verification

- `arch.TestOperationalScriptsAreRegistered` — directory-derived over `scripts/`.
- `arch.TestDatabaseDroppingScripts_RefuseTheLiveDatabase` — any script issuing
  `DROP DATABASE` must compare against the live database.

| Control | Result |
|---|---|
| Remove the live-database guard | fails, naming `dr-drill.sh` |
| Add an unregistered `scripts/rogue-fix.sh` | fails, naming the script |
| Register a script that does not exist | fails: *"no longer exists"* |
| Vacuity | both guards fail explicitly if no scripts / no dropping script is found |
| Live positive control | a throwaway target still runs |
