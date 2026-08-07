import { getJSON, patchJSON, postJSON } from './api';

// Typed contract for GET/POST/PATCH /v1/admin/risks{,/register,/{id}}
// (E-SEC-UI.3, E8.4a, ADR-SOC-008). Mirrors
// internal/httpapi/handlers_risks.go's riskDTO (lines 21-34) and
// riskRegisterEntryDTO (lines 273-277) field-for-field, and
// internal/httpapi/openapi.yaml's Risk/RiskRegisterEntry schemas
// (lines 5386-5468) — kept hand-written, like vulnerabilities.ts, until a
// second consumer of the generated spec appears.

/** domain.RiskLikelihood's closed, five-member scale (internal/domain/risk.go:19-25), in its own natural (1-based rank) order. */
export const RISK_LIKELIHOODS = ['rare', 'unlikely', 'possible', 'likely', 'almost_certain'] as const;
export type RiskLikelihood = (typeof RISK_LIKELIHOODS)[number];

/** domain.RiskImpact's closed, five-member scale (internal/domain/risk.go:67-73), in its own natural (1-based rank) order. */
export const RISK_IMPACTS = ['negligible', 'minor', 'moderate', 'major', 'severe'] as const;
export type RiskImpact = (typeof RISK_IMPACTS)[number];

/** domain.RiskStatus's closed lifecycle (internal/domain/risk.go:134-151). */
export const RISK_STATUSES = ['open', 'mitigating', 'accepted', 'closed'] as const;
export type RiskStatus = (typeof RISK_STATUSES)[number];

/** domain.RiskTreatment's closed, OPTIONAL disposition (internal/domain/risk.go:110-115). */
export const RISK_TREATMENTS = ['mitigate', 'accept', 'transfer', 'avoid'] as const;
export type RiskTreatment = (typeof RISK_TREATMENTS)[number];

/** domain.RiskSeverityBand — the computed label GET .../register attaches (internal/domain/risk.go:254-259). */
export const RISK_SEVERITY_BANDS = ['critical', 'high', 'medium', 'low'] as const;
export type RiskSeverityBand = (typeof RISK_SEVERITY_BANDS)[number];

export interface RiskDTO {
  risk_id: string;
  title: string;
  description: string;
  /** Open-but-bounded operator label — NOT a closed enum. Blank means uncategorised. */
  category: string;
  likelihood: RiskLikelihood;
  impact: RiskImpact;
  status: RiskStatus;
  /** Absent means no treatment decision has been recorded yet. */
  treatment?: RiskTreatment;
  /** Absent means this risk names no Configuration Item. */
  asset_id?: string;
  row_version: number;
  created_at: string;
  updated_at: string;
}

/** GET /v1/admin/risks/register's own row shape — never stored, recomputed on every call. */
export interface RiskRegisterEntryDTO {
  risk: RiskDTO;
  /** likelihoodRank(risk.likelihood) * impactRank(risk.impact), each rank 1-based (1..5) — see domain.RiskRepository.Register's doc comment. */
  score: number;
  band: RiskSeverityBand;
}

/**
 * The bound on a single board fetch — this board fetches one bounded page
 * and paginates/sorts client-side over it, the same "no keyset chase" choice
 * VULN_FINDING_LIST_CAP documents (vulnerabilities.ts), matching the
 * server's own `limit` maximum (openapi.yaml:2576, `maximum: 500`) with
 * headroom below it.
 */
export const RISK_LIST_CAP = 200;

export interface ListRisksOptions {
  /** Omitted or '' returns every status — the server's own default. */
  status?: RiskStatus | '';
  category?: string;
  limit?: number;
}

/** GET /v1/admin/risks (handlers_risks.go:104-129). */
export function listRisks(opts: ListRisksOptions = {}, signal?: AbortSignal): Promise<{ items: RiskDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(opts.limit ?? RISK_LIST_CAP));
  if (opts.status) p.set('status', opts.status);
  if (opts.category) p.set('category', opts.category);
  return getJSON<{ items: RiskDTO[] }>(`/v1/admin/risks?${p.toString()}`, signal);
}

/**
 * GET /v1/admin/risks/register (handlers_risks.go:295-319): the caller's
 * OPEN (non-closed: open, mitigating or accepted) risks ranked by
 * likelihood x impact score — see domain.RiskRepository.Register's doc
 * comment for the exact ranking. Takes no status/category filter — the
 * endpoint itself accepts none beyond `limit` (openapi.yaml:2628-2631).
 */
export function getRiskRegister(
  limit: number = RISK_LIST_CAP,
  signal?: AbortSignal,
): Promise<{ items: RiskRegisterEntryDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(limit));
  return getJSON<{ items: RiskRegisterEntryDTO[] }>(`/v1/admin/risks/register?${p.toString()}`, signal);
}

/** GET /v1/admin/risks/{id} (handlers_risks.go:174-189). */
export function getRisk(riskId: string, signal?: AbortSignal): Promise<RiskDTO> {
  return getJSON<RiskDTO>(`/v1/admin/risks/${encodeURIComponent(riskId)}`, signal);
}

/** `domain.assetTypePattern`-shaped: lower-case snake_case (internal/domain/risk.go:230, reusing internal/domain/asset.go:155). */
const RISK_CATEGORY_PATTERN = /^[a-z][a-z0-9_]*$/;
export const RISK_CATEGORY_MAX_LENGTH = 100;

/** `undefined` when v is empty (not yet answered) or valid; an error string otherwise. */
export function riskCategoryError(v: string): string | undefined {
  const trimmed = v.trim();
  if (trimmed.length === 0) return undefined;
  if (trimmed.length > RISK_CATEGORY_MAX_LENGTH) return `Must be at most ${RISK_CATEGORY_MAX_LENGTH} characters.`;
  if (!RISK_CATEGORY_PATTERN.test(trimmed)) {
    return 'Must be lower-case snake_case, e.g. operational, compliance, security.';
  }
  return undefined;
}

// ---------------------------------------------------------------------------
// Write actions (E-SEC-UI.3, ADR-ACT-001's pattern). Every mutation below
// sends the `row_version` last read back to the server (optimistic locking)
// and returns the server's own post-mutation DTO.

/**
 * POST /v1/admin/risks — createRiskRequest (handlers_risks.go:59-67). Always
 * minted `open`; `risk_id` is minted server-side and never sent. `asset_id`,
 * when supplied, must already name a Configuration Item visible to the
 * caller's tenant, re-verified server-side (404 otherwise).
 */
export interface CreateRiskInput {
  title: string;
  description?: string;
  category?: string;
  likelihood: RiskLikelihood;
  impact: RiskImpact;
  treatment?: RiskTreatment;
  asset_id?: string;
}

export function createRisk(input: CreateRiskInput, signal?: AbortSignal): Promise<RiskDTO> {
  return postJSON<RiskDTO>('/v1/admin/risks', input, signal);
}

/**
 * The non-lifecycle fields `patchRiskRequest` accepts in a single call
 * (handlers_risks.go:81-100). `status` is deliberately absent here — a
 * transition goes through `setRiskStatus` instead, never combined with these
 * in the same request (the server refuses a request that tries both).
 * `treatment`/`asset_id` follow the tri-state rule domain.RiskPatch
 * documents: omit to leave unchanged, `''` to clear, a non-empty value to
 * set.
 */
export interface RiskPatchInput {
  title?: string;
  description?: string;
  category?: string;
  likelihood?: RiskLikelihood;
  impact?: RiskImpact;
  treatment?: string;
  asset_id?: string;
}

/**
 * PATCH /v1/admin/risks/{id} with one or more non-status fields.
 * `rowVersion` MUST be the value last read for this risk; a stale one
 * returns 409 (`ErrVersionMismatch`).
 */
export function patchRisk(
  riskId: string,
  patch: RiskPatchInput,
  rowVersion: number,
  signal?: AbortSignal,
): Promise<RiskDTO> {
  return patchJSON<RiskDTO>(`/v1/admin/risks/${encodeURIComponent(riskId)}`, { row_version: rowVersion, ...patch }, signal);
}

/**
 * The whole lifecycle, as data — a client-side mirror of
 * `domain.riskTransitions` (internal/domain/risk.go:173-178). Kept in
 * lock-step with the Go table deliberately, the same reasoning
 * VULN_FINDING_TRANSITIONS' own doc comment gives: this is the ONLY thing
 * that gates which transition buttons the console offers.
 */
export const RISK_TRANSITIONS: Record<RiskStatus, RiskStatus[]> = {
  open: ['mitigating', 'accepted', 'closed'],
  mitigating: ['accepted', 'closed', 'open'],
  accepted: ['open', 'closed'],
  closed: ['open'],
};

/** The legal next statuses from `status` — never anything `RISK_TRANSITIONS` doesn't list. */
export function legalRiskTransitions(status: RiskStatus): RiskStatus[] {
  return RISK_TRANSITIONS[status];
}

/**
 * PATCH /v1/admin/risks/{id} — a lifecycle transition, ALONE in the request
 * body (handlers_risks.go:226-232). `rowVersion` MUST be the value last read
 * for this risk; a stale one, or an illegal move, both return 409
 * (handlers_risks.go:254-262 maps both ErrVersionMismatch and
 * ErrInvalidTransition to 409 — the same "refetch, never blind-retry"
 * contract transitionIncident's own doc comment states).
 */
export function setRiskStatus(
  riskId: string,
  rowVersion: number,
  status: RiskStatus,
  signal?: AbortSignal,
): Promise<RiskDTO> {
  return patchJSON<RiskDTO>(`/v1/admin/risks/${encodeURIComponent(riskId)}`, { row_version: rowVersion, status }, signal);
}
