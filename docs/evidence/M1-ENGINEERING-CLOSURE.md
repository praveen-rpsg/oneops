# M1 Engineering Closure Record

**Milestone:** M1 — Configuration Registry
**Date:** 2026-07-22
**Board:** OneOps Engineering Closure Board (post-Finality Judgment; non-constitutional)
**Scope:** execute the engineering + governance actions required by the ratified
constitutional precedent (INT-1…INT-5). No constitutional question reopened.

---

## 1. Remaining Work Matrix (result)

| Item | Category | Status | Verification |
|---|---|---|---|
| Version-binding (git commit + tag) | Engineering | **DONE** | initial commit `4d6456e`; tag `v0.1.0-m1` |
| Dependency security remediation | Engineering/Security | **DONE** | `govulncheck` → 0 reachable |
| Toolchain upgrade (Go 1.23 → 1.25) | Engineering | **DONE** | `go.mod` go 1.25.0; image on `golang:1.25-alpine` |
| Lint under new toolchain | Engineering | **DONE** | golangci-lint v2.5.0 → 0 issues (config migrated to v2) |
| Configuration Object attachment | Governance | **DONE** | `docs/CONFIGURATION-OBJECT-M1.yaml` (ONEOPS-CFG-0013) |
| SBOM | Governance | **DONE** | `docs/evidence/m1-sbom.cdx.json` (CycloneDX 1.7, 38 components) |
| Evidence package | Governance | **DONE** | this record + SBOM committed |
| Ratification to Active/Current-Baseline | Governance (Council, §8) | **PENDING** | Constitutional Architecture Council action |
| Production deployment / DR | Operations | **NOT STARTED** | out of M1 scope |

## 2. Security Closure Matrix

| Class | Before | Action | After | Verification |
|---|---|---|---|---|
| Dependency — pgx SQLi `GO-2026-5004` | reachable | pgx → **v5.9.2** | cleared | govulncheck |
| Dependency — jwt DoS `GO-2025-3553` | reachable | jwt → **v5.2.2** | cleared | govulncheck |
| Dependency — otel/sdk ACE `GO-2026-4394` | reachable | otel → **v1.44.0** | cleared | govulncheck |
| Dependency — otlp mem `GO-2026-4985` | reachable | otlptracehttp → **v1.44.0** | cleared | govulncheck |
| Supply-chain — grpc authz `CVE-2026-33186` (CRIT) | present | grpc → **v1.82.1** | cleared | trivy image |
| Dependency — x/text `GO-2026-5970` (surfaced by upgrade) | reachable | x/text → **v0.39.0** | cleared | govulncheck |
| Toolchain — Go stdlib CVEs | present (go1.23) | rebuild on **Go 1.25** | cleared | trivy image |
| Application | clean | — | clean | gitleaks (no leaks) |
| Infrastructure | clean | — | clean | trivy config (0) |

**Result:** `govulncheck` **0 reachable** (was 4) · Trivy image **0** HIGH/CRITICAL (was 38) · Trivy fs **0** (was 23) · **CI `security` job would PASS** (was FAIL).

## 3. Governance Closure Matrix

| Item | State |
|---|---|
| Configuration Object (ONEOPS-CFG-0013) | **Complete** |
| CFG-ID assigned | **Complete** (next in the §7 CFG-0001…0012 series) |
| Evidence package | **Complete** |
| SBOM | **Complete** |
| Coverage evidence | **Complete** (domain 90.2 / observability 96.7 / httpapi 88.3 / auth 87.9 / config 87.0 / store-postgres 71.6; aggregate 76.2) |
| Performance evidence | **Complete** (CRUD p95 ~4ms local) |
| Audit record | **Complete** (Configuration Audit Log) |
| Version tag | **Complete** (`v0.1.0-m1`) |
| Ratification (Authority→Active) | **Not done** — reserved to the Council (§8) |

## 4. Operational Readiness Matrix

| Item | State |
|---|---|
| Build reproducibility | **Ready** (committed + tagged; `go mod verify` passes) |
| Immutable release artifact | **Ready** (tagged image `oneops/controlplane:m1`; distroless, nonroot, read-only) |
| Rollback | **Needs Work** (deploy pipeline not exercised) |
| Deployment | **Needs Work** (ArgoCD/Helm authored; not deployed) |
| Monitoring / Logging / Tracing | **Ready** (Prometheus + slog + OTel wired) |
| Secrets | **Needs Work** (fail-closed on default HMAC secret in non-dev recommended) |
| Runbooks / DR | **Not Applicable** at M1 (M9 scope) |

## 5. Go / No-Go (independent axes — not combined)

| Axis | Status |
|---|---|
| **Engineering** | **GO** — build/vet/lint/tests green; security clean; version-bound |
| **Governance** | **GO for registration** — Config Object attached; **ratification pending Council** |
| **Operational** | **NO-GO** — deployment/DR/secrets hardening outstanding (M9 scope) |
| **Production** | **NO-GO** — depends on Operational |
| **Constitutional** | **SETTLED** — precedent final (INT-1…INT-5); not re-opened |

## 6. Risk Register (residual)

| ID | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| E-1 | otel 1.28→1.44 major jump introduces subtle tracing behavior change | Low | Low | full test suite green; OTel API stable across 1.x |
| E-2 | Default HMAC secret used in a real deployment | Low | High | fail-closed in non-dev (recommended, not yet implemented) |
| E-3 | Local p95 mistaken for capacity guarantee | Med | Med | load test before M9 SLO claims |
| E-4 | New CVEs land against pinned deps over time | Med | Med | CI `security` job now green and gating; renovate/monitoring |

## 7. Final Milestone Closure Checklist

- [x] Version-bound (commit + tag)
- [x] Dependency vulnerabilities remediated (govulncheck 0 reachable)
- [x] Toolchain patched (Go 1.25) and image rebuilt
- [x] Lint/vet/tests green on new toolchain
- [x] SBOM generated and committed
- [x] Configuration Object attached (ONEOPS-CFG-0013)
- [x] Evidence package committed
- [x] Audit Log updated
- [ ] Ratification to Active/Current-Baseline — **reserved to the Constitutional Architecture Council (§8)**
- [ ] Production deployment / DR — **M9 scope, out of M1**

**Engineering closure: COMPLETE.** Remaining items are (a) a governance ratification act reserved to the Council and (b) operational/production work outside M1 scope.
