# OneOps Governance Platform — Rollback Guide

## Binary rollback (preferred)
Because v1.x migrations are additive, a **newer schema is compatible with an older
binary**. To roll back the application:
1. Redeploy the previous image/tag.
2. Do **not** run down-migrations for a simple binary rollback — the extra tables
   are inert to the older binary.

## Schema rollback (only if a migration must be reverted)
Down-migrations live in `internal/store/migrate/rollback/` and DROP exactly what
their forward migration created:
- `20260726000001_policies.down.sql`
- `20260725000001_webhook_replay.down.sql`
- `20260724000001_webhooks.down.sql`
- `20260723000001_audit.down.sql`
- `20260722000002_graph.down.sql`
- `20260722000001_init.down.sql`

Apply the relevant `*.down.sql` in **reverse** dependency order, then delete the
corresponding rows from `schema_migrations`.

### WARNING — audit is append-only
`20260723000001_audit.down.sql` drops the tamper-evident audit chain. **Never**
run it against a system of record without an approved constitutional decision and
a verified backup; audit history deletion is forbidden by the Constitution (§8).

## Data safety
- Down-migrations for webhooks/replay/policies destroy operational (non-
  constitutional) state only — delivery history, replay jobs, policy executions.
- Governance and audit data are not touched by those three rollbacks.
