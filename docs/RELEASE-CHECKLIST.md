# OneOps Governance Platform v1.0 GA — Release Checklist

Every item is objectively verifiable. Command shown where applicable.

## Code & build
- [x] `gofmt -l internal cmd sdk pkg` clean (excl. pre-existing).
- [x] `go vet ./...` clean.
- [x] `golangci-lint run ./...` → 0 issues.
- [x] `go build ./...` succeeds.
- [x] No import cycles: `go list ./...` succeeds.
- [x] Reproducible build via `make build` (version injected by `-ldflags`).

## Tests
- [x] `go test ./... -race` — all unit packages pass.
- [ ] `go test -tags integration -race ./internal/store/postgres/... ./internal/httpapi/...`
      with `TEST_DATABASE_URL` set — all pass (run in CI `integration` job).
- [x] Integration suite compiles: `go vet -tags integration ./...`.

## Migrations
- [x] `atlas.sum` regenerated for all 6 migrations.
- [ ] `atlas migrate validate` green (CI `migrations` job).
- [x] Down-migrations present for every forward migration.

## CI/CD
- [x] CI runs unit (race+coverage), integration (Postgres, fail-on-skip),
      migration validation, lint, security (gitleaks+trivy), docker build.
- [x] `release/**` branches trigger CI.

## Security
- [x] Production config guard rejects insecure defaults (JWT, DB URL, sslmode, auth-off).
- [x] Admin endpoints require `artifacts:admin`; pprof off by default.
- [x] Secrets redacted in diagnostics/webhook responses.
- [x] Secret-at-rest risk documented & accepted (disaster-recovery.md).

## Documentation
- [x] Deployment, upgrade, rollback, disaster-recovery guides.
- [x] Runbooks: audit-integrity, event-delivery, policy-automation, timeline-and-compliance.
- [x] CHANGELOG.md and VERSION (1.0.0).

## Release artifact
- [ ] All platform code committed to `release/v1.0.0` (no uncommitted platform changes).
- [ ] Signed, annotated tag `v1.0.0` created from the reviewed release branch.
- [ ] Container image built and pushed from the tagged commit (`make docker`).
- [ ] SBOM generated for the release image.

## Post-deploy verification (staging → prod)
- [ ] `GET /readyz` = ready; `GET /v1/admin/status` healthy.
- [ ] `POST /v1/admin/integrity/run` → `healthy: true`.
- [ ] `GET /metrics` exposes governance/audit/webhook/policy/timeline/compliance series.
