import { getJSON } from './api';

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
