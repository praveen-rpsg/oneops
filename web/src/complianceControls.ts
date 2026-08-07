import { getJSON, patchJSON, postJSON } from './api';

// Typed contract for GET/POST/PATCH /v1/admin/compliance-controls{,/{id},
// /{id}/evidence} (E-SEC-UI.3, E8.4b, ADR-SOC-009). Mirrors
// internal/httpapi/handlers_compliance_controls.go's complianceControlDTO
// (lines 23-33), controlEvidenceDTO (lines 46-52) and
// complianceControlWithEvidenceDTO (lines 65-68) field-for-field, and
// internal/httpapi/openapi.yaml's ComplianceControl/ControlEvidence/
// ComplianceControlWithEvidence schemas (lines 5469-5545) — kept
// hand-written, like risks.ts, until a second consumer of the generated spec
// appears.

/** domain.ComplianceControlStatus's closed implementation lifecycle (internal/domain/compliance_control.go:17-35). */
export const COMPLIANCE_CONTROL_STATUSES = ['not_implemented', 'in_progress', 'implemented', 'not_applicable'] as const;
export type ComplianceControlStatus = (typeof COMPLIANCE_CONTROL_STATUSES)[number];

/** domain.ControlEvidenceKind's closed vocabulary (internal/domain/compliance_control.go:317-327). */
export const CONTROL_EVIDENCE_KINDS = ['url', 'note', 'attestation'] as const;
export type ControlEvidenceKind = (typeof CONTROL_EVIDENCE_KINDS)[number];

export interface ComplianceControlDTO {
  control_id: string;
  /** The compliance framework this control belongs to (e.g. SOC2, ISO27001) — open-but-bounded, NOT a closed enum. Immutable after creation. */
  framework: string;
  /** The control's own identifier WITHIN framework (e.g. CC6.1 under SOC2). Immutable after creation. */
  control_ref: string;
  title: string;
  description: string;
  status: ComplianceControlStatus;
  row_version: number;
  created_at: string;
  updated_at: string;
}

/** One append-only row of a control's evidence trail — never updated or deleted after it is written. */
export interface ControlEvidenceDTO {
  evidence_id: string;
  kind: ControlEvidenceKind;
  value: string;
  /** The actor who filed this evidence, sourced from the authenticated request subject — never client-supplied. */
  recorded_by: string;
  recorded_at: string;
}

/** GET /v1/admin/compliance-controls/{id}'s response shape — the control plus its bounded evidence trail, server-ordered oldest first (ComplianceControlRepository.Evidence's own doc comment). */
export interface ComplianceControlWithEvidenceDTO extends ComplianceControlDTO {
  evidence: ControlEvidenceDTO[];
}

/**
 * The bound on a single board fetch — this board fetches one bounded page
 * and paginates/sorts client-side over it, the same "no keyset chase" choice
 * RISK_LIST_CAP documents (risks.ts), matching the server's own `limit`
 * maximum (openapi.yaml:2701, `maximum: 500`) with headroom below it.
 */
export const COMPLIANCE_CONTROL_LIST_CAP = 200;

export interface ListComplianceControlsOptions {
  framework?: string;
  /** Omitted or '' returns every status — the server's own default. */
  status?: ComplianceControlStatus | '';
  limit?: number;
}

/** GET /v1/admin/compliance-controls (handlers_compliance_controls.go:98-123). */
export function listComplianceControls(
  opts: ListComplianceControlsOptions = {},
  signal?: AbortSignal,
): Promise<{ items: ComplianceControlDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(opts.limit ?? COMPLIANCE_CONTROL_LIST_CAP));
  if (opts.framework) p.set('framework', opts.framework);
  if (opts.status) p.set('status', opts.status);
  return getJSON<{ items: ComplianceControlDTO[] }>(`/v1/admin/compliance-controls?${p.toString()}`, signal);
}

/** GET /v1/admin/compliance-controls/{id} (handlers_compliance_controls.go:165-193) — control + evidence, embedded, in one round trip. */
export function getComplianceControl(controlId: string, signal?: AbortSignal): Promise<ComplianceControlWithEvidenceDTO> {
  return getJSON<ComplianceControlWithEvidenceDTO>(`/v1/admin/compliance-controls/${encodeURIComponent(controlId)}`, signal);
}

/** `domain.complianceControlFrameworkPattern` (internal/domain/compliance_control.go:113): case-preserving, letters/digits/'_'/'-' only, must start with a letter. */
const COMPLIANCE_CONTROL_FRAMEWORK_PATTERN = /^[A-Za-z][A-Za-z0-9_-]*$/;
export const COMPLIANCE_CONTROL_FRAMEWORK_MAX_LENGTH = 100;

/** `domain.complianceControlRefPattern` (internal/domain/compliance_control.go:123): letters/digits/'.'/'_'/'-', must start with a letter or digit. */
const COMPLIANCE_CONTROL_REF_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
export const COMPLIANCE_CONTROL_REF_MAX_LENGTH = 50;

/** `undefined` when v is empty (not yet answered) or valid; an error string otherwise. */
export function complianceControlFrameworkError(v: string): string | undefined {
  const trimmed = v.trim();
  if (trimmed.length === 0) return undefined;
  if (trimmed.length > COMPLIANCE_CONTROL_FRAMEWORK_MAX_LENGTH) {
    return `Must be at most ${COMPLIANCE_CONTROL_FRAMEWORK_MAX_LENGTH} characters.`;
  }
  if (!COMPLIANCE_CONTROL_FRAMEWORK_PATTERN.test(trimmed)) {
    return "Must start with a letter and contain only letters, digits, '_' or '-', e.g. SOC2, ISO27001, PCI-DSS.";
  }
  return undefined;
}

export function complianceControlRefError(v: string): string | undefined {
  const trimmed = v.trim();
  if (trimmed.length === 0) return undefined;
  if (trimmed.length > COMPLIANCE_CONTROL_REF_MAX_LENGTH) {
    return `Must be at most ${COMPLIANCE_CONTROL_REF_MAX_LENGTH} characters.`;
  }
  if (!COMPLIANCE_CONTROL_REF_PATTERN.test(trimmed)) {
    return "Must start with a letter or digit and contain only letters, digits, '.', '_' or '-', e.g. CC6.1.";
  }
  return undefined;
}

// ---------------------------------------------------------------------------
// Write actions (E-SEC-UI.3, ADR-ACT-001's pattern). Every mutation below
// returns the server's own post-mutation DTO.

/**
 * POST /v1/admin/compliance-controls — createComplianceControlRequest
 * (handlers_compliance_controls.go:73-78). Always minted `not_implemented`;
 * `control_id` is minted server-side and never sent. `framework`/
 * `control_ref` are FIXED at creation — see ComplianceControlDTO's own doc
 * comment; a duplicate (framework, control_ref) within this tenant returns
 * 409.
 */
export interface CreateComplianceControlInput {
  framework: string;
  control_ref: string;
  title: string;
  description?: string;
}

export function createComplianceControl(
  input: CreateComplianceControlInput,
  signal?: AbortSignal,
): Promise<ComplianceControlDTO> {
  return postJSON<ComplianceControlDTO>('/v1/admin/compliance-controls', input, signal);
}

/**
 * The non-lifecycle fields `patchComplianceControlRequest` accepts in a
 * single call (handlers_compliance_controls.go:85-94). `framework`/
 * `control_ref` are deliberately absent — immutable after Create. `status`
 * is likewise absent — a transition goes through `setComplianceControlStatus`
 * instead, never combined with these in the same request.
 */
export interface ComplianceControlPatchInput {
  title?: string;
  description?: string;
}

export function patchComplianceControl(
  controlId: string,
  patch: ComplianceControlPatchInput,
  rowVersion: number,
  signal?: AbortSignal,
): Promise<ComplianceControlDTO> {
  return patchJSON<ComplianceControlDTO>(
    `/v1/admin/compliance-controls/${encodeURIComponent(controlId)}`,
    { row_version: rowVersion, ...patch },
    signal,
  );
}

/**
 * The whole lifecycle, as data — a client-side mirror of
 * `domain.complianceControlTransitions` (internal/domain/
 * compliance_control.go:63-70). Kept in lock-step with the Go table
 * deliberately, the same reasoning RISK_TRANSITIONS' own doc comment gives.
 */
export const COMPLIANCE_CONTROL_TRANSITIONS: Record<ComplianceControlStatus, ComplianceControlStatus[]> = {
  not_implemented: ['in_progress', 'not_applicable'],
  in_progress: ['implemented', 'not_applicable', 'not_implemented'],
  implemented: ['in_progress', 'not_implemented'],
  not_applicable: ['not_implemented'],
};

/** The legal next statuses from `status` — never anything `COMPLIANCE_CONTROL_TRANSITIONS` doesn't list. */
export function legalComplianceControlTransitions(status: ComplianceControlStatus): ComplianceControlStatus[] {
  return COMPLIANCE_CONTROL_TRANSITIONS[status];
}

/**
 * PATCH /v1/admin/compliance-controls/{id} — a lifecycle transition, ALONE
 * in the request body (handlers_compliance_controls.go:228-235).
 * `rowVersion` MUST be the value last read for this control; a stale one, or
 * an illegal move, both return 409 (handlers_compliance_controls.go:242-253
 * maps both ErrVersionMismatch and ErrInvalidTransition to 409 — the same
 * "refetch, never blind-retry" contract setRiskStatus's own doc comment
 * states).
 */
export function setComplianceControlStatus(
  controlId: string,
  rowVersion: number,
  status: ComplianceControlStatus,
  signal?: AbortSignal,
): Promise<ComplianceControlDTO> {
  return patchJSON<ComplianceControlDTO>(
    `/v1/admin/compliance-controls/${encodeURIComponent(controlId)}`,
    { row_version: rowVersion, status },
    signal,
  );
}

export const CONTROL_EVIDENCE_VALUE_MAX_LENGTH = 4000;

/**
 * POST /v1/admin/compliance-controls/{id}/evidence — addControlEvidenceRequest
 * (handlers_compliance_controls.go:266-269, addControlEvidence:275-307).
 * APPEND-ONLY: there is no edit or delete of a filed record anywhere in this
 * client. Carries NO `row_version` — the endpoint takes none (mirrors
 * `deleteSecurityRule`'s identical "no optimistic lock on this verb" shape,
 * confirmed against the Go handler before this was written).
 * `recorded_by`/`evidence_id` are server-minted and never sent.
 */
export function addControlEvidence(
  controlId: string,
  kind: ControlEvidenceKind,
  value: string,
  signal?: AbortSignal,
): Promise<ControlEvidenceDTO> {
  return postJSON<ControlEvidenceDTO>(
    `/v1/admin/compliance-controls/${encodeURIComponent(controlId)}/evidence`,
    { kind, value },
    signal,
  );
}
