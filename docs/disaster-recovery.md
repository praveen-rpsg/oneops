# OneOps Governance Platform — Disaster Recovery

## Status

| Capability | State |
|---|---|
| Logical backup | **Implemented** — `scripts/db-backup.sh`, `make db-backup` |
| Restore | **Implemented** — `scripts/db-restore.sh` |
| Verified round trip | **Implemented and executed** — `make dr-drill` |
| PITR / WAL archiving | **Not implemented** — requires a managed database; see below |
| Off-site retention | **Not implemented** — requires an environment |

The drill last passed against PostgreSQL 16 with 16 tables verified and both
`audit_event` append-only triggers confirmed present after restore. Until PITR
exists, **RPO equals the interval between `db-backup` runs**, not zero.

## Tooling

```bash
make db-backup   # verified logical backup into ./backups
make dr-drill    # backup -> restore into a throwaway DB -> verify -> drop
```

Both run inside a `postgres:16` container. This is not a convenience: `pg_dump`
refuses to dump a server newer than itself, so a mismatched client silently
turns the backup job into a failing one. The production backup CronJob must use
the image matching the database major version for the same reason.

## What the drill asserts

1. The dump restores at all.
2. Every table's row count survives the round trip.
3. The `audit_event` append-only triggers are present afterwards. These enforce
   the State Model §8 guarantee that audit history cannot be deleted; a restore
   that lost them would leave audit history mutable with no other symptom.

## Backup principles
- Take **consistent full backups** of the single PostgreSQL database (governance
  + audit are co-located and must be backed up together — ADR-AUDIT-005).
- Add PITR (WAL archiving) once a managed database exists, so a restore can
  target a precise point in time.
- Back up configuration/secrets management separately (JWT keys, DB creds).

## Restore procedure
1. Restore the database from backup/PITR to a new instance
   (`scripts/db-restore.sh <dump> <target-url>`).
2. Point the platform at the restored DB (`ONEOPS_DB_URL`); start with
   `ONEOPS_AUTO_MIGRATE=true` (idempotent — no-ops if schema matches).
3. **Re-verify audit integrity immediately**: `POST /v1/admin/integrity/run`, then
   `GET /v1/admin/integrity`. A restore can introduce chain drift; a break here is
   a P1 (see runbooks/audit-integrity.md).
4. Confirm `GET /v1/admin/status` healthy and dependencies up.

## Integrity after restore
The audit chain is per governed object and hash-linked. If a restore is partial or
crosses a write boundary, the verification scheduler (or the on-demand sweep) will
detect a break with `chain_id`, `first_break_seq`, and reason. Do not mutate the
chain; snapshot the affected rows and escalate.

## RTO/RPO guidance
- RPO is bounded by your WAL/backup cadence (recommend continuous WAL archiving).
- RTO is dominated by DB restore time plus one integrity sweep.
- Event-delivery/replay/policy state is reconstructable operational data; audit and
  governance are the systems of record and take restore priority.

## Secrets at rest
Webhook secrets and policy action configs live in the DB. Restore encryption-at-
rest volumes and rotate exposed webhook secrets (`PATCH .../webhooks/{id}` with
`rotate_secret: true`) after any suspected disclosure.
