# ADR-SOC-004 — An IOC is a tenant-scoped, curated watchlist row (reference data), not a reified threat entity

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-07 |
| **Related** | ADR-SOC-001/002/003 (observations, detection config, detector), ADR-SOC-002's config-mirror pattern, ADR-HARD-003 (DELETE-no-row_version), `internal/domain/ioc.go`, `docs/PLATFORM-BUILD-PLAN.md` E8.2a. **Consumed by E8.2b** (IOC-match detection — separate). |

## Context

E8.2 adds a second detection mode alongside E8.1's threshold/window: matching
security observations against known Indicators of Compromise (IOCs). That
requires a curated indicator list first. "Behavioral/anomaly" detection (the
other half of the plan's E8.2 line) needs baselines/ML and is deferred to the
AI track (E13) — E8.2 here is IOC matching only.

## Decision

`ioc` is tenant-owned REFERENCE DATA — a curated watchlist an operator
maintains, the same legitimate-stored-data category as `alert_rule` /
`maintenance_window`, NOT a reified Threat/Event false-noun. CRUD at
`POST/GET/PATCH/DELETE /v1/admin/iocs` (PermAdmin), mirroring the security_rule
CRUD discipline: server-minted `ioc_id`, tenant-scoped `appPool`, FORCE RLS
`tenant_isolation`, PATCH requires `row_version` (409), DELETE none
(ADR-HARD-003).

Fields: `indicator_type` (closed enum `ip|domain|url|file_hash|email`),
`indicator_value` (normalized — trim always; lowercase for domain/url/email;
ip/file_hash trimmed only, since a hash's hex case and an IPv6 literal are
meaningful), `severity` (the `IncidentSeverity` a match will raise, consumed by
E8.2b), `enabled`, `description`/`source` (provenance). `indicator_type`/
`indicator_value` are not patchable (delete+recreate).

**Uniqueness is tenant-scoped:** `UNIQUE (tenant_id, indicator_type,
indicator_value)` — a tenant can't hold the same indicator twice (→ 409), but
two tenants can independently watch the same indicator (proven by test). This
is the same cross-tenant-channel discipline `TestUniquenessIsScopedToTenant`
enforces platform-wide.

## Consequences

**Guaranteed.** Operators curate a tenant-isolated indicator watchlist; the
RLS/uniqueness/tenant-column arch guards sweep `ioc` in automatically. Nothing
matches against it yet.

**Deferred.** IOC-match detection → incident is E8.2b. Behavioral/anomaly
detection is out of scope (E13/AI). No enrichment/threat-intel-feed ingestion
(indicators are operator-entered; automated feed sync is a later story).

## Enforcement

- `TestIOCIsolation_RLSByTenant` + `TestIOCStore_DuplicateIndicatorWithinTenantConflicts`
  (`internal/store/postgres/ioc_store_integration_test.go`) — the isolation +
  tenant-scoped-uniqueness gates.
- The platform RLS/uniqueness/tenant-column guards cover `ioc`; contract
  bijection keeps routes/schema in lockstep.
- `indicator_type`/`indicator_value` must stay non-patchable; the unique
  constraint must stay tenant-scoped (never global).
