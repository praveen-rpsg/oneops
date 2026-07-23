# Runbook: Event Delivery & Replay

**Scope:** Webhook delivery (PRS-018) and replay/recovery (PRS-019). Both are
decoupled event consumers that tail the committed audit log; they never affect
governance or audit.

## Metrics
| Metric | Meaning |
| --- | --- |
| `oneops_webhook_deliveries_total` | successful deliveries |
| `oneops_webhook_failures_total` | failed attempts |
| `oneops_webhook_retries_total` | scheduled retries |
| `oneops_webhook_delivery_latency_seconds` | attempt latency |
| `oneops_webhook_active` | enabled webhooks |
| `oneops_webhook_deadletters` | current dead-letter count |
| `oneops_webhook_replay_jobs_total` / `_events_total` / `_duration_seconds` | replay activity |

## Suggested alerts
- Dead-letter growth: `increase(oneops_webhook_failures_total[15m])` high, or
  `oneops_webhook_deadletters > 0` sustained.
- No deliveries while events flow: correlate with `oneops_governance_operations_total`.

## Common tasks
- Inspect a delivery: `GET /v1/admin/webhooks/{id}/deliveries/{deliveryID}`
  (payload, headers, attempts, status).
- List dead-letters: `GET /v1/admin/webhooks/deadletters`.
- Retry one: `POST /v1/admin/webhooks/{id}/deliveries/{deliveryID}/retry`.
- Retry all dead-letters: `POST /v1/admin/webhooks/deadletters/retry`.
- Replay a window/ids: `POST /v1/admin/webhooks/{id}/replay` → poll
  `GET /v1/admin/webhooks/replay/jobs/{jobID}`.

## Notes
- Delivery is at-least-once; subscribers must dedupe on `X-OneOps-Delivery` /
  `event_id`. Payloads are HMAC-SHA256 signed (`X-OneOps-Signature`).
- Retention prunes terminal deliveries older than `ONEOPS_WEBHOOK_RETENTION_HOURS`;
  pending/failed are never deleted.
