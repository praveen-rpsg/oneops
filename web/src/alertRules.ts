import { deleteJSON, getJSON, patchJSON, postJSON } from './api';

// Typed contract for GET /v1/admin/alert-rules{,/{id}}.
// Mirrors internal/httpapi/handlers_alert_rules.go's alertRuleDTO exactly —
// field names and shape, not a paraphrase. Kept hand-written, like
// incidents.ts/noc.ts, until a second consumer of the generated spec appears.
//
// current_incident_id is deliberately NOT part of this contract:
// domain.AlertRule carries it (internal/domain/alertrule.go), but
// toAlertRuleDTO does not serialize it — the field is not part of the
// existing HTTP surface this story reuses unchanged. There is therefore no
// "linked incident" column on the alerts board; see ADR-NOC-005's "what this
// story does not do" for the honest account of that gap.

export const ALERT_SEVERITIES = ['critical', 'warning', 'info'] as const;
export type AlertSeverity = (typeof ALERT_SEVERITIES)[number];

export const ALERT_RULE_STATES = ['ok', 'firing'] as const;
export type AlertRuleState = (typeof ALERT_RULE_STATES)[number];

export const ALERT_COMPARATORS = ['gt', 'lt', 'gte', 'lte'] as const;
export type AlertComparator = (typeof ALERT_COMPARATORS)[number];

export const ALERT_SYMPTOM_CLASSES = ['availability', 'resource', 'unspecified'] as const;
export type AlertSymptomClass = (typeof ALERT_SYMPTOM_CLASSES)[number];

export interface AlertRuleDTO {
  rule_id: string;
  asset_id: string;
  metric: string;
  comparator: AlertComparator;
  threshold: number;
  for_duration_seconds: number;
  severity: AlertSeverity;
  symptom_class: AlertSymptomClass;
  enabled: boolean;
  last_state: AlertRuleState;
  last_transition_at?: string;
  flap_dwell_seconds: number;
  row_version: number;
  created_at: string;
  updated_at: string;
}

/**
 * The bound on a single board fetch — the same "one page, no keyset chasing"
 * posture incidents.ts' INCIDENT_LIST_CAP documents, for the same reason:
 * this list endpoint carries no next-page cursor in its response body.
 */
export const ALERT_RULE_LIST_CAP = 100;

export interface ListAlertRulesOptions {
  limit?: number;
}

export function listAlertRules(
  opts: ListAlertRulesOptions = {},
  signal?: AbortSignal,
): Promise<{ items: AlertRuleDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(opts.limit ?? ALERT_RULE_LIST_CAP));
  return getJSON<{ items: AlertRuleDTO[] }>(`/v1/admin/alert-rules?${p.toString()}`, signal);
}

export function getAlertRule(ruleId: string, signal?: AbortSignal): Promise<AlertRuleDTO> {
  return getJSON<AlertRuleDTO>(`/v1/admin/alert-rules/${encodeURIComponent(ruleId)}`, signal);
}

// ---------------------------------------------------------------------------
// Write actions (E-ACT.2, ADR-ACT-002) — the write-action pattern ADR-ACT-001
// established (row_version round-trip, 409-triggers-refetch, error
// surfacing), applied to alert_rule's own three write endpoints. The
// contract facts below were confirmed against
// `internal/httpapi/handlers_alert_rules.go` before any of this was written.

/**
 * The bounds `domain.AlertRule.Validate` enforces
 * (`internal/domain/alertrule.go`), restated here so the create/edit forms
 * reject an invalid value before it ever reaches the server — the same
 * "restate, don't derive" posture `incidents.ts`' `INCIDENT_TRANSITIONS`
 * documents its own doc comment reasoning for (no runtime "what is valid"
 * field exists to fetch this from instead).
 */
export const ALERT_RULE_FOR_DURATION_MIN = 1;
export const ALERT_RULE_FOR_DURATION_MAX = 24 * 3600;
export const ALERT_RULE_FLAP_DWELL_MIN = 0;
export const ALERT_RULE_FLAP_DWELL_MAX = 24 * 3600;
export const ALERT_RULE_METRIC_MAX_LENGTH = 100;
/** Lower-case snake_case — mirrors `domain.telemetryMetricPattern` (`^[a-z][a-z0-9_]*$`). */
export const ALERT_RULE_METRIC_PATTERN = /^[a-z][a-z0-9_]*$/;

/** `undefined` when v is empty (not yet answered) or valid; an error string otherwise. */
export function alertRuleAssetIdError(v: string): string | undefined {
  return v.length > 0 && v.trim().length === 0 ? 'Asset ID is required.' : undefined;
}

export function alertRuleMetricError(v: string): string | undefined {
  if (v.length === 0) return undefined;
  const trimmed = v.trim();
  if (trimmed.length === 0) return 'Metric is required.';
  if (trimmed.length > ALERT_RULE_METRIC_MAX_LENGTH) return `Must be at most ${ALERT_RULE_METRIC_MAX_LENGTH} characters.`;
  if (!ALERT_RULE_METRIC_PATTERN.test(trimmed)) return 'Must be lower-case snake_case, e.g. cpu_utilization.';
  return undefined;
}

export function alertRuleThresholdError(v: string): string | undefined {
  if (v.trim().length === 0) return undefined;
  return Number.isFinite(Number(v)) ? undefined : 'Must be a number.';
}

export function alertRuleForDurationError(v: string): string | undefined {
  if (v.trim().length === 0) return undefined;
  const n = Number(v);
  if (!Number.isInteger(n) || n < ALERT_RULE_FOR_DURATION_MIN || n > ALERT_RULE_FOR_DURATION_MAX) {
    return `Must be a whole number of seconds between ${ALERT_RULE_FOR_DURATION_MIN} and ${ALERT_RULE_FOR_DURATION_MAX} (24 hours).`;
  }
  return undefined;
}

/** Empty is valid here — flap_dwell_seconds is optional; the server defaults it. */
export function alertRuleFlapDwellError(v: string): string | undefined {
  if (v.trim().length === 0) return undefined;
  const n = Number(v);
  if (!Number.isInteger(n) || n < ALERT_RULE_FLAP_DWELL_MIN || n > ALERT_RULE_FLAP_DWELL_MAX) {
    return `Must be a whole number of seconds between ${ALERT_RULE_FLAP_DWELL_MIN} and ${ALERT_RULE_FLAP_DWELL_MAX} (24 hours).`;
  }
  return undefined;
}

export interface CreateAlertRuleInput {
  asset_id: string;
  metric: string;
  comparator: AlertComparator;
  threshold: number;
  for_duration_seconds: number;
  severity: AlertSeverity;
  symptom_class: AlertSymptomClass;
  enabled: boolean;
  flap_dwell_seconds?: number;
}

/**
 * POST /v1/admin/alert-rules — createAlertRuleRequest (handlers_alert_rules.go).
 * `asset_id` must already name a Configuration Item visible to the caller's
 * tenant, re-verified server-side (404 otherwise). `rule_id` is minted
 * server-side and never sent.
 */
export function createAlertRule(input: CreateAlertRuleInput, signal?: AbortSignal): Promise<AlertRuleDTO> {
  return postJSON<AlertRuleDTO>('/v1/admin/alert-rules', input, signal);
}

/**
 * The fields `patchAlertRuleRequest` accepts (handlers_alert_rules.go).
 * `asset_id`/`metric` are deliberately absent — `domain.AlertRulePatch`'s own
 * doc comment: what a rule watches is fixed at creation; delete and recreate
 * instead. Also used for the enable/disable action (a patch touching only
 * `enabled`).
 */
export interface AlertRulePatchInput {
  comparator?: AlertComparator;
  threshold?: number;
  for_duration_seconds?: number;
  severity?: AlertSeverity;
  symptom_class?: AlertSymptomClass;
  enabled?: boolean;
  flap_dwell_seconds?: number;
}

/**
 * PATCH /v1/admin/alert-rules/{id}. `rowVersion` MUST be the value last read
 * for this rule; a stale one returns `409` (`ErrVersionMismatch` —
 * `patchAlertRule`'s handler maps it to 409, matching the incident
 * transition/assign endpoints, unlike the governance surface's 412) — see
 * ADR-ACT-002.
 */
export function patchAlertRule(
  ruleId: string,
  patch: AlertRulePatchInput,
  rowVersion: number,
  signal?: AbortSignal,
): Promise<AlertRuleDTO> {
  return patchJSON<AlertRuleDTO>(
    `/v1/admin/alert-rules/${encodeURIComponent(ruleId)}`,
    { row_version: rowVersion, ...patch },
    signal,
  );
}

/**
 * DELETE /v1/admin/alert-rules/{id}.
 *
 * CONFIRMED CONTRACT FACT (ADR-ACT-002), not assumed: unlike
 * `patchAlertRule`, this endpoint takes **no `row_version` and no request
 * body at all** — `deleteAlertRule`'s Go handler
 * (`internal/httpapi/handlers_alert_rules.go`) calls
 * `s.alertRules.Delete(ctx, id)`, which has no optimistic-lock parameter in
 * `domain.AlertRuleRepository` either. There is therefore no way for this
 * call to detect "the rule changed since I loaded its detail" the way
 * `patchAlertRule`/`transitionIncident` can — a real, load-bearing gap
 * recorded here rather than a `row_version` argument fabricated to look
 * consistent with the other two write endpoints.
 */
export function deleteAlertRule(ruleId: string, signal?: AbortSignal): Promise<void> {
  return deleteJSON(`/v1/admin/alert-rules/${encodeURIComponent(ruleId)}`, signal);
}
