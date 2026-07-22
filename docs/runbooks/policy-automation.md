# Runbook: Policy Automation

**Scope:** PRS-020. Policies react to committed governance events via an isolated
consumer + executor. A policy failure can never affect Governance, Audit, Events,
or Replay.

## Metrics
`oneops_policy_executions_total`, `oneops_policy_failures_total`,
`oneops_policy_retries_total`, `oneops_policy_execution_duration_seconds`,
`oneops_policy_active`.

## Suggested alerts
- `increase(oneops_policy_failures_total[15m])` high → action endpoint degraded.
- Dead-letter executions accumulating (inspect via executions endpoint).

## Common tasks
- Manage policies: `GET/POST /v1/admin/policies`, `PATCH/DELETE /v1/admin/policies/{id}`.
- Inspect executions: `GET /v1/admin/policies/{id}/executions`.
- Dry-run an action: `POST /v1/admin/policies/{id}/test`.

## Actions
Built-in: `http` (implemented), `notification` (logs by default), `email` and
`command` (interface-only — inject a backend to enable; otherwise return
"not configured"). Executions retry with backoff up to the policy `max_retries`,
then dead-letter. Each action runs under a recover so a panicking action is
captured on the execution record only.
