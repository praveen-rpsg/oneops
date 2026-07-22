# Changelog

All notable changes to the OneOps Governance Platform are documented here.
This project adheres to Semantic Versioning; the REST surface is versioned `/v1`.

## [1.0.0] — GA

First General Availability release. Delivers PRS-001 through PRS-022.

### Platform (frozen for 1.0)
- Constitutional Configuration Registry and Domain Model (M1/M2).
- Dependency graph and Authority Resolution Engine (M2/M3).
- Governance Operations Engine — 7 of the 12 §8 operations (ratification, approval,
  suspension, deprecation, withdrawal, archiving, deletion). Extension, replacement,
  amendment, baseline-freeze, and historical-preservation are **not yet implemented**
  and return `ErrUnsupportedOperation` (documented v1.0 limitation).
- Audit Integrity — tamper-evident, hash-chained, append-only audit (ADR-AUDIT-003/004).
- **Atomic governance + audit commit** in one transaction (ADR-AUDIT-005).
- Operational hardening: verification scheduler, diagnostics, health, metrics, tracing.
- REST + Administration APIs; official Go SDK.
- Event delivery (signed webhooks), event replay & recovery, policy automation.
- Read-only execution timeline and compliance/evidence engine.

### Release hardening (this release)
- CI now runs PostgreSQL-backed integration tests (fails on skip), migration
  validation (`atlas migrate validate`), race tests, and coverage reporting.
- Added integration tests for the webhook, policy, and timeline stores; fixed the
  stale governance integration test to the current atomic engine signature.
- Regenerated `atlas.sum` for all six migrations; added rollback (down) scripts
  for the webhooks, replay, and policies migrations.
- Completed operational documentation: deployment, upgrade, rollback, disaster
  recovery, and per-subsystem runbooks.

### Known limitations / accepted risks
- Five §8 operations unimplemented (see above).
- Webhook secrets and policy action configs are stored unencrypted in the platform
  database; mitigated operationally via encryption-at-rest and network isolation
  (see docs/disaster-recovery.md). Storage redesign is out of scope for 1.0.
