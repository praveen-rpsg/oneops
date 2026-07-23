# Runbook: Execution Timeline & Compliance

**Scope:** PRS-021 (timeline) and PRS-022 (compliance/evidence). Both are strictly
READ-ONLY read models composed from existing persisted data; they participate in
no execution and write nothing.

## Metrics
`oneops_timeline_queries_total`, `oneops_timeline_query_duration_seconds`,
`oneops_compliance_queries_total`, `oneops_evidence_exports_total`,
`oneops_compliance_query_duration_seconds`.

## Timeline
- By event: `GET /v1/admin/timeline/{eventID}`
- By governance object: `GET /v1/admin/governance/{id}/timeline`
- By replay job: `GET /v1/admin/replay/{jobID}/timeline`
- By policy execution: `GET /v1/admin/policies/{id}/timeline`
- Filters: `from`, `to`, `component`, `status`, `offset`, `limit`. Ordering is
  deterministic (timestamp → component → correlation key).

## Compliance & evidence
- Summary: `GET /v1/admin/compliance/{governanceID}`
- Checks: `GET /v1/admin/compliance/{governanceID}/checks`
- Evidence bundle: `GET /v1/admin/compliance/{governanceID}/evidence`
  (JSON, or reproducible ZIP with `?format=zip`)
- Fleet report: `GET /v1/admin/compliance/reports`

Evidence is deterministic (only `generated_at` varies); the ZIP is reproducible
(entry mtime pinned to `generated_at`). Checks: audit-chain-verified,
no-failed-integrity-verification, audit-events-present, governance-lifecycle-
complete, required-approvals-present, policy-executions-completed.

## For auditors
Evidence is composed from the immutable, hash-linked audit record plus operational
history; it introduces no new state and can be regenerated at any time.
