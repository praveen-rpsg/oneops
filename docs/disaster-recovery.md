# OneOps Governance Platform — Disaster Recovery

## Backup
- Take **consistent full backups** of the single PostgreSQL database (governance
  + audit are co-located and must be backed up together — ADR-AUDIT-005).
- Use PITR (WAL archiving) so a restore can target a precise point in time.
- Back up configuration/secrets management separately (JWT keys, DB creds).

## Restore procedure
1. Restore the database from backup/PITR to a new instance.
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
