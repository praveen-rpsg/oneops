import { deleteJSON, getJSON, patchJSON, postJSON } from './api';
import type { IncidentSeverity } from './incidents';

// Typed contract for GET/POST/PATCH/DELETE /v1/admin/security-rules
// (E-SEC-UI.2). Mirrors internal/httpapi/handlers_security_rules.go's
// securityRuleDTO (lines 21-33) and openapi.yaml's SecurityRule/
// CreateSecurityRuleRequest/PatchSecurityRuleRequest schemas (lines
// 5090-5184) field-for-field, and internal/domain/security_rule.go's bounds
// — kept hand-written, like alertRules.ts, until a second consumer of the
// generated spec appears.

/** domain.ObservationSeverity's closed vocabulary (internal/domain/security_observation.go:49-53) — the scale min_severity counts on. */
export const OBSERVATION_SEVERITIES = ['info', 'low', 'medium', 'high', 'critical'] as const;
export type ObservationSeverity = (typeof OBSERVATION_SEVERITIES)[number];

export const SECURITY_RULE_STATES = ['ok', 'firing'] as const;
export type SecurityRuleState = (typeof SECURITY_RULE_STATES)[number];

/**
 * security_rule.incident_severity uses the INCIDENT severity vocabulary
 * (domain.SecurityRule.IncidentSeverity's own doc comment), the exact same
 * closed set incidents.ts' INCIDENT_SEVERITIES already carries — re-exported
 * here rather than redefined, so the two never drift apart.
 */
export { INCIDENT_SEVERITIES } from './incidents';
export type { IncidentSeverity } from './incidents';

export interface SecurityRuleDTO {
  rule_id: string;
  asset_id: string;
  observation_type: string;
  min_severity: ObservationSeverity;
  window_seconds: number;
  threshold_count: number;
  incident_severity: IncidentSeverity;
  enabled: boolean;
  last_state: SecurityRuleState;
  last_transition_at?: string;
  current_incident_id?: string;
  row_version: number;
  created_at: string;
  updated_at: string;
}

/**
 * The bound on a single board fetch — the same "one page, no keyset chasing"
 * posture ALERT_RULE_LIST_CAP documents, for the same reason: this list
 * endpoint carries no next-page cursor in its response body.
 */
export const SECURITY_RULE_LIST_CAP = 100;

export interface ListSecurityRulesOptions {
  limit?: number;
}

export function listSecurityRules(
  opts: ListSecurityRulesOptions = {},
  signal?: AbortSignal,
): Promise<{ items: SecurityRuleDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(opts.limit ?? SECURITY_RULE_LIST_CAP));
  return getJSON<{ items: SecurityRuleDTO[] }>(`/v1/admin/security-rules?${p.toString()}`, signal);
}

export function getSecurityRule(ruleId: string, signal?: AbortSignal): Promise<SecurityRuleDTO> {
  return getJSON<SecurityRuleDTO>(`/v1/admin/security-rules/${encodeURIComponent(ruleId)}`, signal);
}

// ---------------------------------------------------------------------------
// Write actions — the write-action pattern ADR-ACT-001/ADR-ACT-002
// established (row_version round-trip, 409-triggers-refetch, error
// surfacing), applied to security_rule's own three write endpoints. The
// contract facts below were confirmed against
// `internal/httpapi/handlers_security_rules.go` before any of this was
// written.

/** The bounds `domain.SecurityRule.Validate` enforces (internal/domain/security_rule.go), restated here so the create/edit forms reject an invalid value before it ever reaches the server. */
export const SECURITY_RULE_WINDOW_SECONDS_MIN = 1;
export const SECURITY_RULE_WINDOW_SECONDS_MAX = 24 * 3600;
export const SECURITY_RULE_THRESHOLD_COUNT_MIN = 1;
export const SECURITY_RULE_THRESHOLD_COUNT_MAX = 1_000_000;
export const SECURITY_RULE_OBSERVATION_TYPE_MAX_LENGTH = 100;
/** Lower-case snake_case — mirrors `domain.observationTypePattern` (== `assetTypePattern`, `^[a-z][a-z0-9_]*$`). */
export const SECURITY_RULE_OBSERVATION_TYPE_PATTERN = /^[a-z][a-z0-9_]*$/;

/** `undefined` when v is empty (not yet answered) or valid; an error string otherwise. */
export function securityRuleAssetIdError(v: string): string | undefined {
  return v.length > 0 && v.trim().length === 0 ? 'Asset ID is required.' : undefined;
}

export function securityRuleObservationTypeError(v: string): string | undefined {
  if (v.length === 0) return undefined;
  const trimmed = v.trim();
  if (trimmed.length === 0) return 'Observation type is required.';
  if (trimmed.length > SECURITY_RULE_OBSERVATION_TYPE_MAX_LENGTH) {
    return `Must be at most ${SECURITY_RULE_OBSERVATION_TYPE_MAX_LENGTH} characters.`;
  }
  if (!SECURITY_RULE_OBSERVATION_TYPE_PATTERN.test(trimmed)) {
    return 'Must be lower-case snake_case, e.g. port_scan, malware_detected.';
  }
  return undefined;
}

export function securityRuleWindowSecondsError(v: string): string | undefined {
  if (v.trim().length === 0) return undefined;
  const n = Number(v);
  if (!Number.isInteger(n) || n < SECURITY_RULE_WINDOW_SECONDS_MIN || n > SECURITY_RULE_WINDOW_SECONDS_MAX) {
    return `Must be a whole number of seconds between ${SECURITY_RULE_WINDOW_SECONDS_MIN} and ${SECURITY_RULE_WINDOW_SECONDS_MAX} (24 hours).`;
  }
  return undefined;
}

export function securityRuleThresholdCountError(v: string): string | undefined {
  if (v.trim().length === 0) return undefined;
  const n = Number(v);
  if (!Number.isInteger(n) || n < SECURITY_RULE_THRESHOLD_COUNT_MIN || n > SECURITY_RULE_THRESHOLD_COUNT_MAX) {
    return `Must be a whole number between ${SECURITY_RULE_THRESHOLD_COUNT_MIN} and ${SECURITY_RULE_THRESHOLD_COUNT_MAX}.`;
  }
  return undefined;
}

export interface CreateSecurityRuleInput {
  asset_id: string;
  observation_type: string;
  min_severity: ObservationSeverity;
  window_seconds: number;
  threshold_count: number;
  incident_severity: IncidentSeverity;
  enabled: boolean;
}

/**
 * POST /v1/admin/security-rules — createSecurityRuleRequest
 * (handlers_security_rules.go:60-68). `asset_id` must already name a
 * Configuration Item visible to the caller's tenant, re-verified
 * server-side (404 otherwise). `rule_id` is minted server-side and never
 * sent.
 */
export function createSecurityRule(input: CreateSecurityRuleInput, signal?: AbortSignal): Promise<SecurityRuleDTO> {
  return postJSON<SecurityRuleDTO>('/v1/admin/security-rules', input, signal);
}

/**
 * The fields `patchSecurityRuleRequest` accepts
 * (handlers_security_rules.go:71-78). `asset_id`/`observation_type` are
 * deliberately absent — `domain.SecurityRulePatch`'s own doc comment: what a
 * rule watches is fixed at creation; delete and recreate instead.
 * `last_state`/`current_incident_id` are also absent: only the future
 * detector (E8.1b-2) sets them. Also used for the enable/disable action (a
 * patch touching only `enabled`).
 */
export interface SecurityRulePatchInput {
  min_severity?: ObservationSeverity;
  window_seconds?: number;
  threshold_count?: number;
  incident_severity?: IncidentSeverity;
  enabled?: boolean;
}

/**
 * PATCH /v1/admin/security-rules/{id}. `rowVersion` MUST be the value last
 * read for this rule; a stale one returns `409` (`ErrVersionMismatch` —
 * `patchSecurityRule`'s handler maps it to 409, matching alert_rule's own
 * write path).
 */
export function patchSecurityRule(
  ruleId: string,
  patch: SecurityRulePatchInput,
  rowVersion: number,
  signal?: AbortSignal,
): Promise<SecurityRuleDTO> {
  return patchJSON<SecurityRuleDTO>(
    `/v1/admin/security-rules/${encodeURIComponent(ruleId)}`,
    { row_version: rowVersion, ...patch },
    signal,
  );
}

/**
 * DELETE /v1/admin/security-rules/{id}.
 *
 * CONFIRMED CONTRACT FACT (ADR-HARD-003), not assumed: like
 * `deleteAlertRule`, this endpoint takes **no `row_version` and no request
 * body at all** — `deleteSecurityRule`'s Go handler
 * (`internal/httpapi/handlers_security_rules.go`) calls
 * `s.securityRules.Delete(ctx, id)`, which has no optimistic-lock parameter
 * in `domain.SecurityRuleRepository` either.
 */
export function deleteSecurityRule(ruleId: string, signal?: AbortSignal): Promise<void> {
  return deleteJSON(`/v1/admin/security-rules/${encodeURIComponent(ruleId)}`, signal);
}
