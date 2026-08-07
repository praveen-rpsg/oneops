# ADR-SOC-005 — IOC-match detection is a value-based, window-tiled sibling worker reusing the security-incident correlation

| | |
|---|---|
| **Status** | Accepted |
| **Date** | 2026-08-07 |
| **Related** | ADR-SOC-003 (SecurityDetector + `FindOrCreateOpenSecurityIncident` reused here), ADR-SOC-004 (the `ioc` watchlist + normalization), `internal/security/ioc_matcher.go`, `docs/PLATFORM-BUILD-PLAN.md` E8.2b. Completes E8.2 (IOC matching); behavioral/anomaly detection remains deferred to E13/AI. |

## Context

E8.2a gave a tenant-scoped IOC watchlist; E8.2b turns it into detection. IOC
matching differs in shape from E8.1's threshold detection (match a value, not
count over a window), so it is its own pass rather than folded into the
SecurityDetector.

## Decision

A sibling leader-gated worker `internal/security/ioc_matcher.go` (`IOCMatcher`,
mirroring `Detector`'s Config/Store/Run/RunOnce), reusing the E8.1b-2
security-incident correlation unchanged.

- **Value-based match**: for each candidate indicator type, each observation
  attribute value is normalized with the SAME `domain.NormalizeIOCIndicatorValue`
  used at IOC-write time and looked up in an in-memory index of the tenant's
  enabled indicators. Done Go-side because normalization is type-conditional
  (case-fold domain/url/email; trim-only ip/file_hash) and can't be one SQL
  predicate. **Type-aware field-key mapping** (only compare `src_ip` against
  `ip`-typed IOCs) is DEFERRED — an honest bound; value match can in principle
  match a value in an unexpected field.
- **Window-tiled processing**: trailing window == the pass `Interval`,
  `observed_at > from AND observed_at <= to`, so consecutive ticks tile (each
  observation evaluated ~once) — no note-spam. A crash re-running a tick with an
  unchanged clock could re-note once; accepted as the same "duplicate over
  silent loss" trade the evaluator already documents.
- **Reuses the existing correlation**: `FindOrCreateOpenSecurityIncident` — one
  open security incident per (tenant, asset), shared with threshold detection,
  under the existing `ux_incident_open_security_per_asset` no-dup index. No new
  index. On match, create at the matched IOC's severity with a note naming the
  indicator; on link, append the note. **Severity is first-writer-wins** — a
  later IOC match won't escalate an open incident (honest bound).
- **Bounded**: only tenants with enabled IOCs are swept
  (`TenantsWithEnabledIOCs`); IOCs and observations are keyset-paged and capped
  (5000 each). All privileged reads carry an explicit `tenant_id` predicate —
  **0 new privileged-read guard exemptions** (mirrors the E8.1b-2 split-store
  approach).

## Consequences

**Guaranteed.** An observation attribute matching an enabled IOC raises one
tenant-isolated security incident at the IOC's severity; non-matches raise
nothing; disabled IOCs don't match; the same indicator in two tenants matches
each tenant's own observations only (proven, incl. a shared-asset_id fake test).
No duplicate/spam across repeated passes. The threshold-detection and alert
paths are untouched.

**Deferred / bounds.** Type-aware field-key mapping; severity escalation on a
later match; automated threat-intel feed sync (indicators are operator-entered,
E8.2a). Behavioral/anomaly detection stays with E13/AI.

## Enforcement

- `TestIOCMatcher_*` unit + `TestIOCMatcher_EndToEnd_*` integration
  (`internal/security`, `internal/store/postgres`, `//go:build integration`,
  `TEST_DATABASE_URL`) — match/non-match/disabled/normalization/no-dup/
  two-tenant-isolation. `TestPrivilegedReads_AreScopedToATenant` stays at 0 new
  exemptions.
- The matcher must not add a second security-incident index or read/write
  cross-tenant; value normalization must stay in lockstep with
  `domain.NormalizeIOCIndicatorValue`.
