# OneOps Governance Platform — Deployment Guide

## Prerequisites
- PostgreSQL 14+ (16 recommended). Governance and audit **must share one database**
  (operational invariant of ADR-AUDIT-005: the governance mutation and its audit
  append commit in one transaction).
- A container runtime (image built from the repo `Dockerfile`) or the `oneops`
  binary built via `make build`.
- A JWT issuer (HS256 shared secret for dev/test, or RS256/JWKS for production).

## Configuration (12-factor, all via environment)
| Variable | Default | Notes |
| --- | --- | --- |
| `ONEOPS_ENV` | `dev` | Set to `production`/`prod` to enable the fail-fast production guard. |
| `ONEOPS_HTTP_ADDR` | `:8080` | Listen address. |
| `ONEOPS_DB_URL` | dev DSN | **Required in prod**; `sslmode=disable` is rejected in production. |
| `ONEOPS_DB_MAX_CONNS` | 10 | pgx pool size. |
| `ONEOPS_AUTO_MIGRATE` | true | Applies embedded migrations at startup (idempotent). |
| `ONEOPS_AUTH_ENABLED` | true | Must be true in production. |
| `ONEOPS_JWT_HMAC_KEY` | dev secret | The dev default is **rejected** in production. |
| `ONEOPS_JWKS_URL` | "" | RS256/OIDC public keys (preferred for production). |
| `ONEOPS_OTLP_ENDPOINT` | "" | Enables OTel tracing when set. |
| `ONEOPS_AUDIT_VERIFY_INTERVAL_SECONDS` | 300 | Integrity scheduler cadence; 0 disables. |
| `ONEOPS_WEBHOOK_RETENTION_HOURS` | 720 | Terminal-delivery pruning window; 0 disables. |
| `ONEOPS_PPROF_ENABLED` | false | Runtime profiling; keep false in production. |

The **production guard** (`config.validateProduction`) fails startup when
`ONEOPS_ENV` is production and any of: the dev JWT secret, the dev DB URL,
`sslmode=disable`, or `ONEOPS_AUTH_ENABLED=false` are present.

## Startup sequence (automatic)
1. Load + validate config (fail-fast).
2. Open pool, `WaitForDB`.
3. Apply migrations if `ONEOPS_AUTO_MIGRATE=true`.
4. Start HTTP server + background workers (verification scheduler, event relay/
   dispatcher, replay/retention workers, policy consumer/executor).

## Health & readiness
- `GET /healthz` — liveness. `GET /readyz` — readiness (DB ping).
- `GET /metrics` — Prometheus. `GET /v1/admin/status` — full status (admin token).

## Secrets at rest (accepted risk — see disaster-recovery.md)
Webhook signing secrets and policy action configs are stored in the platform
database. Deploy on a Postgres with **encryption at rest** (managed KMS volume)
and network-isolate the database; do not expose it publicly.
