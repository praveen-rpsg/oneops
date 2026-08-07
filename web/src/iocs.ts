import { deleteJSON, getJSON, patchJSON, postJSON } from './api';
import type { IncidentSeverity } from './incidents';

// Typed contract for GET/POST/PATCH/DELETE /v1/admin/iocs (E-SEC-UI.2).
// Mirrors internal/httpapi/handlers_iocs.go's iocDTO (lines 18-27) and
// openapi.yaml's IOC/CreateIOCRequest/PatchIOCRequest schemas (lines
// 5185-5245) field-for-field, and internal/domain/ioc.go's bounds — kept
// hand-written, like securityRules.ts, until a second consumer of the
// generated spec appears.

/** domain.IOCIndicatorType's closed vocabulary (internal/domain/ioc.go:12-18). */
export const IOC_INDICATOR_TYPES = ['ip', 'domain', 'url', 'file_hash', 'email'] as const;
export type IOCIndicatorType = (typeof IOC_INDICATOR_TYPES)[number];

/**
 * ioc.severity uses the INCIDENT severity vocabulary (domain.IOC.Severity's
 * own doc comment), the exact same closed set security_rule.incident_severity
 * draws from — re-exported here rather than redefined.
 */
export { INCIDENT_SEVERITIES } from './incidents';
export type { IncidentSeverity } from './incidents';

export interface IOCDTO {
  ioc_id: string;
  indicator_type: IOCIndicatorType;
  indicator_value: string;
  severity: IncidentSeverity;
  enabled: boolean;
  description: string;
  source: string;
  row_version: number;
  created_at: string;
  updated_at: string;
}

/**
 * The bound on a single board fetch — the same "one page, no keyset
 * chasing" posture SECURITY_RULE_LIST_CAP/ALERT_RULE_LIST_CAP document.
 */
export const IOC_LIST_CAP = 200;

export interface ListIOCsOptions {
  limit?: number;
}

export function listIOCs(opts: ListIOCsOptions = {}, signal?: AbortSignal): Promise<{ items: IOCDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(opts.limit ?? IOC_LIST_CAP));
  return getJSON<{ items: IOCDTO[] }>(`/v1/admin/iocs?${p.toString()}`, signal);
}

export function getIOC(iocId: string, signal?: AbortSignal): Promise<IOCDTO> {
  return getJSON<IOCDTO>(`/v1/admin/iocs/${encodeURIComponent(iocId)}`, signal);
}

// ---------------------------------------------------------------------------
// Write actions — the write-action pattern ADR-ACT-001/ADR-ACT-002
// established, applied to ioc's own three write endpoints. The contract
// facts below were confirmed against `internal/httpapi/handlers_iocs.go`
// before any of this was written.

/** The bounds `domain.IOC.Validate` enforces (internal/domain/ioc.go), restated here so the create/edit forms reject an invalid value before it ever reaches the server. */
export const IOC_INDICATOR_VALUE_MAX_LENGTH = 2048;
export const IOC_DESCRIPTION_MAX_LENGTH = 2000;
export const IOC_SOURCE_MAX_LENGTH = 200;

/** `undefined` when v is empty (not yet answered) or valid; an error string otherwise. */
export function iocIndicatorValueError(v: string): string | undefined {
  if (v.length === 0) return undefined;
  const trimmed = v.trim();
  if (trimmed.length === 0) return 'Indicator value is required.';
  if (trimmed.length > IOC_INDICATOR_VALUE_MAX_LENGTH) {
    return `Must be at most ${IOC_INDICATOR_VALUE_MAX_LENGTH} characters.`;
  }
  return undefined;
}

export function iocDescriptionError(v: string): string | undefined {
  return v.length > IOC_DESCRIPTION_MAX_LENGTH ? `Must be at most ${IOC_DESCRIPTION_MAX_LENGTH} characters.` : undefined;
}

export function iocSourceError(v: string): string | undefined {
  return v.length > IOC_SOURCE_MAX_LENGTH ? `Must be at most ${IOC_SOURCE_MAX_LENGTH} characters.` : undefined;
}

/**
 * Mirrors `domain.NormalizeIOCIndicatorValue` (internal/domain/ioc.go:59-67):
 * domain/url/email are case-insensitive by their own grammar and are
 * lower-cased; ip/file_hash are case-significant and only trimmed. Applied
 * client-side purely so the create form's preview matches what the server
 * will actually store — the server re-normalizes regardless, this is not a
 * security boundary.
 */
export function normalizeIOCIndicatorValue(indicatorType: IOCIndicatorType, value: string): string {
  const v = value.trim();
  switch (indicatorType) {
    case 'domain':
    case 'url':
    case 'email':
      return v.toLowerCase();
    default:
      return v;
  }
}

export interface CreateIOCInput {
  indicator_type: IOCIndicatorType;
  indicator_value: string;
  severity: IncidentSeverity;
  enabled: boolean;
  description?: string;
  source?: string;
}

/**
 * POST /v1/admin/iocs — createIOCRequest (handlers_iocs.go:44-51). `ioc_id`
 * is minted server-side and never sent. Returns 409 (domain.ErrConflict)
 * when this tenant already holds an entry with the same (indicator_type,
 * indicator_value) — the UNIQUE (tenant_id, indicator_type, indicator_value)
 * constraint.
 */
export function createIOC(input: CreateIOCInput, signal?: AbortSignal): Promise<IOCDTO> {
  return postJSON<IOCDTO>('/v1/admin/iocs', input, signal);
}

/**
 * The fields `patchIOCRequest` accepts (handlers_iocs.go:54-60).
 * `indicator_type`/`indicator_value` are deliberately absent —
 * `domain.IOCPatch`'s own doc comment: what an entry watches for is fixed at
 * creation; delete and recreate instead. Also used for the enable/disable
 * action (a patch touching only `enabled`).
 */
export interface IOCPatchInput {
  severity?: IncidentSeverity;
  enabled?: boolean;
  description?: string;
  source?: string;
}

/**
 * PATCH /v1/admin/iocs/{id}. `rowVersion` MUST be the value last read for
 * this entry; a stale one returns `409` (`ErrVersionMismatch`).
 */
export function patchIOC(
  iocId: string,
  patch: IOCPatchInput,
  rowVersion: number,
  signal?: AbortSignal,
): Promise<IOCDTO> {
  return patchJSON<IOCDTO>(`/v1/admin/iocs/${encodeURIComponent(iocId)}`, { row_version: rowVersion, ...patch }, signal);
}

/**
 * DELETE /v1/admin/iocs/{id}.
 *
 * CONFIRMED CONTRACT FACT (ADR-HARD-003), not assumed: like
 * `deleteSecurityRule`/`deleteAlertRule`, this endpoint takes **no
 * `row_version` and no request body at all** — `deleteIOC`'s Go handler
 * (`internal/httpapi/handlers_iocs.go`) calls `s.iocs.Delete(ctx, id)`, which
 * has no optimistic-lock parameter in `domain.IOCRepository` either.
 */
export function deleteIOC(iocId: string, signal?: AbortSignal): Promise<void> {
  return deleteJSON(`/v1/admin/iocs/${encodeURIComponent(iocId)}`, signal);
}
