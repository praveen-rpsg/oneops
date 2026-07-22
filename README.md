# OneOps

The OneOps Engineering Control Plane — the platform that governs how OneOps is
built. This monorepo implements the [Engineering Execution Playbook](../OneOps-Engineering-Execution-Playbook-v1.0.md)
milestone by milestone.

- **M0** — repository bootstrap (runnable skeleton).
- **M1** — **Configuration Registry** (this release): the production system of
  record for OneOps artifacts (Configuration State Model §6), with a REST API,
  PostgreSQL persistence, auth/RBAC, and full observability.

## Quick start

```bash
# 1. Start local dependencies (postgres, nats, redis, opensearch, minio)
make up

# 2. Run the control plane (auto-migrates the database on startup)
make run
# → serves on :8080

# 3. Exercise it (auth is on by default; disable locally for a quick look)
ONEOPS_AUTH_ENABLED=false make run
curl -s localhost:8080/readyz | jq
curl -s -X POST localhost:8080/v1/artifacts -H 'Content-Type: application/json' -d '{
  "artifact":"OneOps-Constitution-Volume-I.md","version":"1.0.0",
  "role":"constitution","lifecycle":"ratified","retention_class":"current_baseline",
  "authority":"active","metadata":{"owner":"platform"}}' | jq
curl -s "localhost:8080/v1/artifacts?role=constitution&limit=10" | jq
```

Requires: Go 1.23+, Docker, Make.

## API

Base path `/v1`. Contract published at **`/openapi.yaml`**; interactive docs at **`/docs`**.

| Method | Path | Notes |
|---|---|---|
| POST | `/v1/artifacts` | Create. `Idempotency-Key` header supported; returns `ETag` + `Location`. |
| POST | `/v1/artifacts/bulk` | Atomic bulk create. |
| GET | `/v1/artifacts` | List. Cursor pagination (`limit`, `cursor`), filters (`role`, `lifecycle`, `authority`), search (`q`). |
| GET | `/v1/artifacts/{id}` | Get. Honors `If-None-Match` (→ `304`). |
| PATCH | `/v1/artifacts/{id}` | Partial update. **Requires `If-Match`** (optimistic locking → `412` on mismatch). |
| DELETE | `/v1/artifacts/{id}` | Delete. |

Infra endpoints: `GET /healthz` (liveness), `GET /readyz` (readiness — DB ping),
`GET /metrics` (Prometheus). Errors are RFC 7807 `application/problem+json`.

### Authentication & authorization

OIDC/JWT bearer tokens (HS256 for dev, RS256/JWKS for OIDC providers). RBAC roles:
`oneops-reader` (read), `oneops-editor` (read+write), `oneops-admin` (all). Set
`ONEOPS_AUTH_ENABLED=false` to bypass auth locally.

## Configuration (environment)

| Var | Default | Purpose |
|---|---|---|
| `ONEOPS_HTTP_ADDR` | `:8080` | listen address |
| `ONEOPS_DB_URL` | `postgres://oneops:dev@localhost:5432/oneops?sslmode=disable` | database DSN |
| `ONEOPS_AUTO_MIGRATE` | `true` | apply embedded migrations on startup |
| `ONEOPS_AUTH_ENABLED` | `true` | enforce JWT/RBAC |
| `ONEOPS_JWT_ISSUER` / `ONEOPS_JWT_AUDIENCE` | `https://oneops.local` / `oneops` | token validation |
| `ONEOPS_JWT_HMAC_KEY` | dev secret | HS256 key (dev/test) |
| `ONEOPS_JWKS_URL` | — | RS256 JWKS endpoint (OIDC) |
| `ONEOPS_OTLP_ENDPOINT` | — | OTLP trace exporter (empty = disabled) |
| `ONEOPS_MAX_PAGE_SIZE` / `ONEOPS_DEFAULT_PAGE_SIZE` | `200` / `50` | pagination bounds |

See `.env.example` for the full list.

## Migrations

Versioned SQL lives in `internal/store/migrate/sql/` (embedded and applied on
startup; validated by Atlas in CI). The `schema_migrations` table tracks applied
versions, so startup is idempotent.

```bash
make migrate-hash      # recompute atlas.sum after adding a migration
make migrate-validate  # validate the migration directory
```

Rollback scripts are kept in `internal/store/migrate/rollback/` (operational use;
outside the Atlas forward-only directory).

## Development

```bash
make help              # list targets
make test              # unit tests: go test -race -cover
make test-integration  # integration tests (needs TEST_DATABASE_URL)
make lint              # golangci-lint
make gen               # regenerate the Go SDK from openapi.yaml
make docker            # build the container image
```

Integration tests spin against a real Postgres:

```bash
export TEST_DATABASE_URL='postgres://oneops:dev@localhost:5432/oneops?sslmode=disable'
make test-integration
```

## Layout

```
cmd/controlplane/          entrypoint (config, DB, tracing, HTTP, shutdown)
internal/domain/           entities, value objects, validation, repo contracts
internal/store/postgres/   pgx repository, pool, idempotency store
internal/store/migrate/    embedded migrations + applier
internal/httpapi/          chi router, handlers, middleware, DTOs, OpenAPI
internal/auth/             JWT verification (HS256/RS256-JWKS), RBAC
internal/observability/    Prometheus metrics, OpenTelemetry tracing
internal/config/           env configuration
deploy/charts/             Helm chart (probes, security context, resources)
infra/                     Terraform (dev env)
```

## Troubleshooting

- **`database not ready within 30s`** — Postgres isn't reachable. Check `make up`
  and `ONEOPS_DB_URL`. Startup waits up to 30s for the DB before migrating.
- **`401 unauthorized`** — missing/invalid bearer token. For local exploration set
  `ONEOPS_AUTH_ENABLED=false`.
- **`412 precondition failed` on PATCH** — stale `If-Match`; GET the object and
  retry with the current `ETag` (row version).
- **`409 conflict` on create** — `(artifact, version)` already exists.

## Operational notes

- **Health:** `/healthz` (liveness) and `/readyz` (readiness — fails if the DB is
  unreachable). Wire these to Kubernetes probes (see the Helm chart).
- **Metrics:** `/metrics` exposes `http_requests_total` and
  `http_request_duration_seconds` labeled by method/route/status.
- **Tracing:** set `ONEOPS_OTLP_ENDPOINT` to export spans (OTLP/HTTP).
- **Graceful shutdown:** SIGTERM drains in-flight requests within
  `ONEOPS_SHUTDOWN_GRACE_SECONDS` and closes the DB pool.
- **Image:** distroless, non-root, read-only root filesystem.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).
