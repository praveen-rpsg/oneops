# SLOs & Alerting

**Scope:** what the control plane commits to, how it's measured, and where the
executable rules live. This is a pointer, not a copy — the PromQL is the
source of truth: `deploy/charts/controlplane/templates/prometheusrule.yaml`.

**Off by default.** The rules ship as a `monitoring.coreos.com/v1
PrometheusRule`, gated by `prometheusRule.enabled` (default `false`), so
`helm install` behavior is unchanged unless a cluster both runs the Prometheus
Operator and opts in (`values-production.yaml` does). If the Operator isn't
running, `helm install` with the flag on will fail at the CRD — that's
intentional; there's no silent no-op path.

## SLIs

| SLI | Definition | Recording rule |
| --- | --- | --- |
| Availability | ratio of `http_requests_total{status=~"5.."}` to all `http_requests_total` | `oneops:http_requests_error_ratio:rate{5m,30m,1h,6h}` |
| Latency | p99 of `http_request_duration_seconds` | `oneops:http_request_duration_seconds:p99rate5m` |

Both are whole-API aggregates (no `route`/`method` split) — a per-route SLO is
a natural extension, not done here (see "Left out" below). With zero traffic
the error-ratio expression is `0/0` (NaN); Prometheus treats NaN comparisons as
false, so a quiet API alerts on nothing, by design.

## Objectives & error budget

Tunable via `values.yaml` → `prometheusRule.slo` (no template edits required):

| Value | Default | Meaning |
| --- | --- | --- |
| `availabilityObjective` | `"99.9"` | 30-day availability target. 0.1% budget ≈ 43m/month. |
| `latencySLOSeconds` | `1.0` | p99 latency ceiling, sustained 5m. |
| `auditVerifyStalenessSeconds` | `600` | 2x the audit-verify scheduler's default interval (300s). |
| `webhookFailureRatio` | `0.05` | Max tolerated webhook delivery failure ratio, 15m window. |
| `policyFailureRatio` | `0.05` | Max tolerated policy execution failure ratio, 15m window. |

## Alert → runbook map

Multi-window, multi-burn-rate availability alerting (Google SRE workbook
method): the fast-burn threshold is `14.4 × (1 - objective)`, the slow-burn
threshold is `6 × (1 - objective)`, both derived from `availabilityObjective`
so one knob tunes both.

| Alert | Severity | Fires on | Runbook |
| --- | --- | --- | --- |
| `OneOpsAvailabilitySLOFastBurn` | critical | 1h+5m error ratio over 14.4x budget | `runbooks/audit-integrity.md#3-alerts-suggested`¹ |
| `OneOpsAvailabilitySLOSlowBurn` | warning | 6h+30m error ratio over 6x budget | same¹ |
| `OneOpsLatencySLOBreach` | warning | p99 > `latencySLOSeconds` for 5m | `deployment.md#health--readiness` |
| `OneOpsAuditIntegrityBroken` | critical | `oneops_audit_integrity_ok == 0` | `runbooks/audit-integrity.md#4-respond-to-an-integrity-break-p1` |
| `OneOpsInvariantBreached` | critical | `oneops_invariant_breached > 0` | `runbooks/audit-integrity.md`¹ |
| `OneOpsAuditVerifierStale` | warning | verification sweep older than `auditVerifyStalenessSeconds` | `runbooks/audit-integrity.md#1-what-runs` |
| `OneOpsDependencyDown` | critical | `oneops_dependency_up == 0` for 2m | `deployment.md#health--readiness` |
| `OneOpsStartupFailures` | warning | any `oneops_startup_failures_total` increase, 15m | `deployment.md#startup-sequence-automatic` |
| `OneOpsShutdownTimeouts` | warning | any `oneops_shutdown_timeouts_total` increase, 15m | `deployment.md#startup-sequence-automatic` |
| `OneOpsWebhookFailureRatioHigh` | warning | failure ratio over `webhookFailureRatio`, 15m, sustained 10m | `runbooks/event-delivery.md#suggested-alerts` |
| `OneOpsWebhookDeadLettersGrowing` | critical | `oneops_webhook_deadletters > 0` for 15m | `runbooks/event-delivery.md#common-tasks` |
| `OneOpsPolicyFailureRatioHigh` | warning | failure ratio over `policyFailureRatio`, 15m, sustained 10m | `runbooks/policy-automation.md#suggested-alerts` |

¹ `docs/runbooks/` has no dedicated doc for the invariant sentinel (ADR-SECURITY-002)
or for platform dependency/startup/shutdown health — those alerts point at the
closest existing operational doc (`audit-integrity.md`, same package and
severity model; `deployment.md`, which documents the startup sequence and
health endpoints). A dedicated `runbooks/platform-health.md` would close this
gap; not written here (out of scope — flagged for the CTO).

## Severity calls that deviate from a literal reading of the brief

- `OneOpsAuditVerifierStale`, `OneOpsStartupFailures`, `OneOpsShutdownTimeouts`
  are `warning`, not `critical`. Each is a leading indicator (a stale sweep, a
  single rollout hiccup) rather than a proven-active outage; paging on them
  would violate "no page-severity alert on a non-actionable condition."
  `audit-integrity.md` itself already classifies verifier staleness as P3.
  `OneOpsAuditIntegrityBroken`, `OneOpsInvariantBreached`, and
  `OneOpsDependencyDown` remain `critical` — each is a live, confirmed
  fail-closed or outage condition.

## What was not alerted on (not measured)

- Per-route/per-endpoint SLOs — the HTTP metrics carry a `route` label, so
  this is a values/expr extension, not a new metric; left out to keep this
  story's rule set to whole-API SLOs.
- Audit-append latency (`oneops_audit_append_duration_seconds`) and governance
  throughput (`oneops_governance_operations_total`) are emitted but have no
  agreed threshold; wiring an alert on an arbitrary number would be
  non-actionable noise. Left for a follow-up once a baseline is observed in
  production.
- Compliance/timeline query metrics (`oneops_*_queries_total`,
  `*_query_duration_seconds`) are read-model observability, not
  availability-critical; no alert proposed.

## Tuning

Override any `prometheusRule.slo.*` value per environment, e.g.:

```yaml
# values-production.yaml
prometheusRule:
  enabled: true
  labels:
    release: kube-prometheus-stack   # match the Prometheus CR's ruleSelector
  slo:
    availabilityObjective: "99.95"
```

Changing `auditVerifyStalenessSeconds` without also changing
`ONEOPS_AUDIT_VERIFY_INTERVAL_SECONDS` (see `docs/deployment.md`) decouples the
alert from the actual sweep cadence — keep them together.
