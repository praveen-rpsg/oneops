import { getJSON, patchJSON, postJSON } from './api';
import type { IncidentDTO } from './incidents';

// Typed contract for GET/PATCH /v1/admin/vuln-findings{,/prioritized,/{id},
// /{id}/remediate} (E-SEC-UI.1, E8.3a/E8.3b, ADR-SOC-007). Mirrors
// internal/httpapi/handlers_vuln_findings.go's vulnFindingDTO (lines 22-41)
// and prioritizedVulnFindingDTO (lines 272-277) field-for-field, and
// internal/httpapi/openapi.yaml's VulnFinding/VulnFindingPrioritized schemas
// (lines 5299-5374) — kept hand-written, like incidents.ts/api.ts, until a
// second consumer of the generated spec appears.

export const VULN_FINDING_SEVERITIES = ['none', 'low', 'medium', 'high', 'critical'] as const;
export type VulnFindingSeverity = (typeof VULN_FINDING_SEVERITIES)[number];

export const VULN_FINDING_STATUSES = ['open', 'remediated', 'accepted_risk', 'false_positive'] as const;
export type VulnFindingStatus = (typeof VULN_FINDING_STATUSES)[number];

export const VULN_FINDING_PRIORITIES = ['critical', 'high', 'medium', 'low'] as const;
export type VulnFindingPriority = (typeof VULN_FINDING_PRIORITIES)[number];

/** domain.AssetCriticality's own closed vocabulary (internal/domain/asset.go:220-226) — the join Prioritized performs. */
export const ASSET_CRITICALITIES = ['critical', 'high', 'medium', 'low', 'unknown'] as const;
export type AssetCriticality = (typeof ASSET_CRITICALITIES)[number];

export interface VulnFindingDTO {
  finding_id: string;
  asset_id: string;
  vuln_id: string;
  title: string;
  severity: VulnFindingSeverity;
  status: VulnFindingStatus;
  first_seen: string;
  last_seen: string;
  scanner: string;
  description: string;
  /** Read-only — set ONLY by POST .../remediate. Absent means no remediation incident is currently linked. */
  remediation_incident_id?: string;
  row_version: number;
  created_at: string;
  updated_at: string;
}

/** GET /v1/admin/vuln-findings/prioritized's own row shape — never stored, recomputed on every call. */
export interface PrioritizedVulnFindingDTO {
  finding: VulnFindingDTO;
  asset_criticality: AssetCriticality;
  priority_score: number;
  priority: VulnFindingPriority;
}

/**
 * The bound on a single board fetch — this board fetches one bounded page and
 * paginates/sorts client-side over it, the same "no keyset chase" choice
 * INCIDENT_LIST_CAP documents (incidents.ts), and matches the server's own
 * `limit` maximum (openapi.yaml:2408, `maximum: 500`) with headroom below it.
 */
export const VULN_FINDING_LIST_CAP = 200;

export interface ListVulnFindingsOptions {
  assetId?: string;
  /** Omitted or '' returns every status — the server's own default. */
  status?: VulnFindingStatus | '';
  severity?: VulnFindingSeverity | '';
  limit?: number;
}

/** GET /v1/admin/vuln-findings (handlers_vuln_findings.go:161-187). */
export function listVulnFindings(
  opts: ListVulnFindingsOptions = {},
  signal?: AbortSignal,
): Promise<{ items: VulnFindingDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(opts.limit ?? VULN_FINDING_LIST_CAP));
  if (opts.assetId) p.set('asset_id', opts.assetId);
  if (opts.status) p.set('status', opts.status);
  if (opts.severity) p.set('severity', opts.severity);
  return getJSON<{ items: VulnFindingDTO[] }>(`/v1/admin/vuln-findings?${p.toString()}`, signal);
}

/**
 * GET /v1/admin/vuln-findings/prioritized (handlers_vuln_findings.go:292-323):
 * the caller's OPEN findings ranked by severity x asset criticality — see
 * domain.VulnFindingRepository.Prioritized's doc comment for the exact
 * ranking. Takes no status/severity filter — the endpoint itself accepts
 * none beyond `limit` (openapi.yaml:2469-2472).
 */
export function listPrioritizedVulnFindings(
  limit: number = VULN_FINDING_LIST_CAP,
  signal?: AbortSignal,
): Promise<{ items: PrioritizedVulnFindingDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(limit));
  return getJSON<{ items: PrioritizedVulnFindingDTO[] }>(`/v1/admin/vuln-findings/prioritized?${p.toString()}`, signal);
}

/** GET /v1/admin/vuln-findings/{id} (handlers_vuln_findings.go:190-205). */
export function getVulnFinding(findingId: string, signal?: AbortSignal): Promise<VulnFindingDTO> {
  return getJSON<VulnFindingDTO>(`/v1/admin/vuln-findings/${encodeURIComponent(findingId)}`, signal);
}

// ---------------------------------------------------------------------------
// Write actions (E-SEC-UI.1, ADR-ACT-001's pattern). Every mutation below
// sends the `row_version` last read back to the server (optimistic locking)
// and returns the server's own post-mutation DTO.

/**
 * The whole lifecycle, as data — a client-side mirror of
 * `domain.vulnFindingTransitions` (internal/domain/vuln_finding.go:103-108).
 * Kept in lock-step with the Go table deliberately, the same reasoning
 * INCIDENT_TRANSITIONS' own doc comment gives: this is the ONLY thing that
 * gates which transition buttons the console offers, and there is no
 * runtime "legal next states" field on vulnFindingDTO to derive it from
 * instead. false_positive -> open is reachable ONLY through this manual
 * path — a scan ingest never performs it (see VulnFindingFalsePositive's own
 * doc comment).
 */
export const VULN_FINDING_TRANSITIONS: Record<VulnFindingStatus, VulnFindingStatus[]> = {
  open: ['remediated', 'accepted_risk', 'false_positive'],
  remediated: ['open'],
  accepted_risk: ['open'],
  false_positive: ['open'],
};

/** The legal next statuses from `status` — never anything `VULN_FINDING_TRANSITIONS` doesn't list. */
export function legalVulnFindingTransitions(status: VulnFindingStatus): VulnFindingStatus[] {
  return VULN_FINDING_TRANSITIONS[status];
}

/**
 * PATCH /v1/admin/vuln-findings/{id} — patchVulnFindingRequest
 * (handlers_vuln_findings.go:213-216). `rowVersion` MUST be the value last
 * read for this finding; a stale one, or an illegal move, both return 409
 * (handlers_vuln_findings.go:250-260 maps both ErrVersionMismatch and
 * ErrInvalidTransition to 409 — the same "refetch, never blind-retry"
 * contract transitionIncident's own doc comment states).
 */
export function setVulnFindingStatus(
  findingId: string,
  rowVersion: number,
  status: VulnFindingStatus,
  signal?: AbortSignal,
): Promise<VulnFindingDTO> {
  return patchJSON<VulnFindingDTO>(
    `/v1/admin/vuln-findings/${encodeURIComponent(findingId)}`,
    { row_version: rowVersion, status },
    signal,
  );
}

/** POST /v1/admin/vuln-findings/{id}/remediate's response (handlers_vuln_findings.go:332-337). */
export interface RemediateVulnFindingResult {
  vuln_finding: VulnFindingDTO;
  incident: IncidentDTO;
}

/**
 * POST /v1/admin/vuln-findings/{id}/remediate (handlers_vuln_findings.go:
 * 339-383). IDEMPOTENT: if the finding already names an OPEN remediation
 * incident, that same incident is returned unchanged — see
 * domain.VulnFindingRepository.Remediate's own doc comment. `rowVersion` MUST
 * be the value last read for this finding; the finding must currently be
 * `open` (409 `ErrVulnFindingNotOpen` otherwise).
 */
export function remediateVulnFinding(
  findingId: string,
  rowVersion: number,
  signal?: AbortSignal,
): Promise<RemediateVulnFindingResult> {
  return postJSON<RemediateVulnFindingResult>(
    `/v1/admin/vuln-findings/${encodeURIComponent(findingId)}/remediate`,
    { row_version: rowVersion },
    signal,
  );
}
