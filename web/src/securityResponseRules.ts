import { deleteJSON, getJSON, patchJSON, postJSON } from './api';
import type { IncidentSeverity } from './incidents';

// Typed contract for GET/POST/PATCH/DELETE /v1/admin/security-response-rules
// (E-SEC-UI.4). Mirrors internal/httpapi/handlers_security_response_rules.go's
// securityResponseRuleDTO (lines 22-33) and openapi.yaml's SecurityResponseRule/
// CreateSecurityResponseRuleRequest/PatchSecurityResponseRuleRequest schemas
// (lines 5551-5615) field-for-field, and internal/domain/security_response_rule.go's
// bounds — kept hand-written, like securityRules.ts, until a second consumer
// of the generated spec appears.

export { INCIDENT_SEVERITIES } from './incidents';
export type { IncidentSeverity } from './incidents';

/**
 * domain.SafeSecurityResponseActionTypes (internal/domain/security_response_rule.go:27)
 * — the SAFE, outbound-only allowlist a rule's action_type is validated
 * against at Create/Update time. This is deliberately NOT the full set
 * policy.DefaultRegistry registers (which also has "email" and "command",
 * arbitrary execution) — only these two ever reach a Select in this console.
 * Destructive/autonomous response is DEFERRED behind the machine-action
 * attestation model (ADR-SOC-010); this UI must never offer a third option.
 */
export const SAFE_ACTION_TYPES = ['http', 'notification'] as const;
export type SecurityResponseActionType = (typeof SAFE_ACTION_TYPES)[number];

export interface SecurityResponseRuleDTO {
  rule_id: string;
  name: string;
  /** IncidentSeverity's own vocabulary (critical|high|medium|low), NOT ObservationSeverity — confirmed at openapi.yaml:5564-5567 and domain.SecurityResponseRule.MinSeverity's own field comment. */
  min_severity: IncidentSeverity;
  asset_id?: string;
  action_type: SecurityResponseActionType;
  /** Opaque JSON, validated only by the action implementation when it runs (policy.ActionSpec.Config's own split) — never by this schema. */
  action_config: Record<string, unknown>;
  enabled: boolean;
  row_version: number;
  created_at: string;
  updated_at: string;
}

/**
 * The bound on a single board fetch — the same "one page, no keyset chasing"
 * posture SECURITY_RULE_LIST_CAP documents, for the same reason: this list
 * endpoint carries no next-page cursor in its response body that this board
 * follows.
 */
export const SECURITY_RESPONSE_RULE_LIST_CAP = 100;

export interface ListSecurityResponseRulesOptions {
  limit?: number;
}

export function listSecurityResponseRules(
  opts: ListSecurityResponseRulesOptions = {},
  signal?: AbortSignal,
): Promise<{ items: SecurityResponseRuleDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(opts.limit ?? SECURITY_RESPONSE_RULE_LIST_CAP));
  return getJSON<{ items: SecurityResponseRuleDTO[] }>(`/v1/admin/security-response-rules?${p.toString()}`, signal);
}

export function getSecurityResponseRule(ruleId: string, signal?: AbortSignal): Promise<SecurityResponseRuleDTO> {
  return getJSON<SecurityResponseRuleDTO>(`/v1/admin/security-response-rules/${encodeURIComponent(ruleId)}`, signal);
}

// ---------------------------------------------------------------------------
// Write actions — the write-action pattern ADR-ACT-001/ADR-ACT-002
// established (row_version round-trip, 409-triggers-refetch, error
// surfacing), applied to security_response_rule's own three write endpoints.
// The contract facts below were confirmed against
// `internal/httpapi/handlers_security_response_rules.go` before any of this
// was written.

/** domain.MaxSecurityResponseRuleNameLength (internal/domain/security_response_rule.go:42). */
export const SECURITY_RESPONSE_RULE_NAME_MAX_LENGTH = 200;
/** domain.MaxSecurityResponseActionConfigLength (internal/domain/security_response_rule.go:50) — action_config is bounded raw JSON bytes, opaque otherwise. */
export const SECURITY_RESPONSE_RULE_ACTION_CONFIG_MAX_LENGTH = 8192;

/** `undefined` when v is empty (not yet answered) or valid; an error string otherwise. */
export function securityResponseRuleNameError(v: string): string | undefined {
  const trimmed = v.trim();
  if (trimmed.length === 0) return undefined;
  if (trimmed.length > SECURITY_RESPONSE_RULE_NAME_MAX_LENGTH) {
    return `Must be at most ${SECURITY_RESPONSE_RULE_NAME_MAX_LENGTH} characters.`;
  }
  return undefined;
}

/** Mirrors securityRuleAssetIdError: only invalid when non-empty but blank after trim — asset_id is OPTIONAL here (nil means "every asset"). */
export function securityResponseRuleAssetIdError(v: string): string | undefined {
  return v.length > 0 && v.trim().length === 0 ? 'Asset ID must not be blank when supplied; leave empty to match every asset.' : undefined;
}

/**
 * The webhook URL the http action config editor collects, built into
 * `{"url": ...}` before submit — the EXACT (and only) key
 * `policy.HTTPAction.Run` reads (internal/policy/actions.go:66-68). Required,
 * non-empty; a light `new URL()` parse check is UI guidance only — the
 * backend itself does not validate URL syntax, only presence.
 */
export function securityResponseRuleWebhookUrlError(v: string): string | undefined {
  const trimmed = v.trim();
  if (trimmed.length === 0) return undefined;
  try {
    new URL(trimmed);
    return undefined;
  } catch {
    return 'Must be a valid URL, e.g. https://example.com/hooks/security.';
  }
}

/**
 * The notification action's config has NO fixed shape server-side:
 * `policy.NotificationAction.Run` (internal/policy/actions.go:148-158) only
 * consults an injected `Notifier` (nil in this deployment's DefaultRegistry
 * wiring — it falls back to a log line and never reads `config` at all), and
 * `domain.SecurityResponseRule.Validate` only requires the bytes be valid
 * JSON under 8192 bytes. So this editor is a documented, honest choice: a
 * raw JSON textarea, not a fabricated fixed-key form — validated for JSON
 * syntax and the same byte bound the domain enforces before it ever reaches
 * the server.
 */
export function securityResponseRuleActionConfigJsonError(raw: string): string | undefined {
  const trimmed = raw.trim();
  if (trimmed.length === 0) return undefined;
  if (new TextEncoder().encode(trimmed).length > SECURITY_RESPONSE_RULE_ACTION_CONFIG_MAX_LENGTH) {
    return `Must be at most ${SECURITY_RESPONSE_RULE_ACTION_CONFIG_MAX_LENGTH} bytes.`;
  }
  try {
    JSON.parse(trimmed);
    return undefined;
  } catch {
    return 'Must be valid JSON.';
  }
}

/** Pulls the `url` a previously-saved `http` rule's action_config carries, for pre-filling the edit form's Webhook URL field. Empty string if absent/malformed. */
export function webhookUrlFromActionConfig(config: Record<string, unknown>): string {
  const url = config.url;
  return typeof url === 'string' ? url : '';
}

/** Builds the exact `{"url": ...}` shape `policy.HTTPAction.Run` reads (actions.go:66-68). */
export function actionConfigFromWebhookUrl(url: string): Record<string, unknown> {
  return { url: url.trim() };
}

/** Pretty-prints a previously-saved `notification` rule's action_config for the JSON textarea. `{}` for an empty/absent config. */
export function actionConfigToJsonText(config: Record<string, unknown>): string {
  if (!config || Object.keys(config).length === 0) return '{}';
  return JSON.stringify(config, null, 2);
}

/** Parses the notification editor's raw text back into the object the request body carries. Empty/whitespace becomes `{}`, matching `NewSecurityResponseRule`'s own default. Caller must have already checked `securityResponseRuleActionConfigJsonError`. */
export function actionConfigFromJsonText(raw: string): Record<string, unknown> {
  const trimmed = raw.trim();
  if (trimmed.length === 0) return {};
  return JSON.parse(trimmed) as Record<string, unknown>;
}

export interface CreateSecurityResponseRuleInput {
  name: string;
  min_severity: IncidentSeverity;
  asset_id?: string;
  action_type: SecurityResponseActionType;
  action_config: Record<string, unknown>;
  enabled: boolean;
}

/**
 * POST /v1/admin/security-response-rules — createSecurityResponseRuleRequest
 * (handlers_security_response_rules.go:53-60). `asset_id`, when supplied,
 * must already name a Configuration Item visible to the caller's tenant,
 * re-verified server-side (404 otherwise). `action_type` MUST be in
 * SAFE_ACTION_TYPES — the server also enforces this (422 otherwise) but this
 * client never offers a third option in the first place. `rule_id` is minted
 * server-side and never sent.
 */
export function createSecurityResponseRule(
  input: CreateSecurityResponseRuleInput,
  signal?: AbortSignal,
): Promise<SecurityResponseRuleDTO> {
  return postJSON<SecurityResponseRuleDTO>('/v1/admin/security-response-rules', input, signal);
}

/**
 * The fields `patchSecurityResponseRuleRequest` accepts
 * (handlers_security_response_rules.go:65-71). `asset_id`/`action_type` are
 * deliberately absent — `domain.SecurityResponseRulePatch`'s own doc comment:
 * a rule that could be silently repointed from "notification" to some future
 * unsafe action via PATCH would defeat the create-time SAFE allowlist check.
 * Delete and recreate instead. Also used for the enable/disable action (a
 * patch touching only `enabled`).
 */
export interface SecurityResponseRulePatchInput {
  name?: string;
  min_severity?: IncidentSeverity;
  action_config?: Record<string, unknown>;
  enabled?: boolean;
}

/**
 * PATCH /v1/admin/security-response-rules/{id}. `rowVersion` MUST be the
 * value last read for this rule; a stale one returns `409`
 * (`ErrVersionMismatch` — the handler maps it to 409, matching
 * security_rule's own write path).
 */
export function patchSecurityResponseRule(
  ruleId: string,
  patch: SecurityResponseRulePatchInput,
  rowVersion: number,
  signal?: AbortSignal,
): Promise<SecurityResponseRuleDTO> {
  return patchJSON<SecurityResponseRuleDTO>(
    `/v1/admin/security-response-rules/${encodeURIComponent(ruleId)}`,
    { row_version: rowVersion, ...patch },
    signal,
  );
}

/**
 * DELETE /v1/admin/security-response-rules/{id}.
 *
 * CONFIRMED CONTRACT FACT (ADR-HARD-003), not assumed: like
 * `deleteSecurityRule`, this endpoint takes **no `row_version` and no
 * request body at all** — `deleteSecurityResponseRule`'s Go handler
 * (`internal/httpapi/handlers_security_response_rules.go:224-239`) calls
 * `s.securityResponseRules.Delete(ctx, id)`, which has no optimistic-lock
 * parameter in `domain.SecurityResponseRuleRepository` either.
 */
export function deleteSecurityResponseRule(ruleId: string, signal?: AbortSignal): Promise<void> {
  return deleteJSON(`/v1/admin/security-response-rules/${encodeURIComponent(ruleId)}`, signal);
}
