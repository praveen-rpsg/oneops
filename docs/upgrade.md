# OneOps Governance Platform — Upgrade Guide

## Compatibility guarantees
- The REST surface is versioned under `/v1` and is additive-only within v1.x.
- Migrations are **additive** (new tables/columns/indexes only). No destructive
  DDL ships in v1.x; the audit tables are append-only (DB triggers enforce it).
- Unknown JSON fields are ignored by the SDK, so newer server fields do not break
  older clients.

## Rolling upgrade (zero-downtime, additive migrations)
1. Verify migration integrity in CI: `atlas migrate validate` (green).
2. Deploy the new image to one replica with `ONEOPS_AUTO_MIGRATE=true`. The
   embedded migrator (`schema_migrations` table) applies pending migrations
   idempotently and in lexical order.
3. Confirm `GET /readyz` and `GET /v1/admin/status` (migration_version matches the
   newest embedded migration).
4. Roll the remaining replicas. Because migrations are additive, old and new
   binaries can run concurrently during the roll.

## Post-upgrade verification
- `GET /v1/admin/integrity` — scheduler healthy, no new failures.
- `POST /v1/admin/integrity/run` — on-demand full sweep; expect `healthy: true`.
- `GET /v1/admin/status` — `healthy: true`, dependencies up.

## Migration ordering (as of v1.0)
`init → graph → audit → webhooks → webhook_replay → policies`
(see `internal/store/migrate/sql/`, checksummed in `atlas.sum`).
