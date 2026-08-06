import { getJSON, postJSON } from './api';

// Typed contract for GET /v1/admin/incidents{,/{id},/{id}/timeline}.
// Mirrors internal/httpapi/handlers_incidents.go's incidentDTO/incidentEventDTO
// exactly — field names and shape, not a paraphrase. Kept hand-written, like
// api.ts/noc.ts, until a second consumer of the generated spec appears.

export const INCIDENT_SEVERITIES = ['critical', 'high', 'medium', 'low'] as const;
export type IncidentSeverity = (typeof INCIDENT_SEVERITIES)[number];

export const INCIDENT_STATUSES = [
  'open',
  'acknowledged',
  'investigating',
  'resolved',
  'closed',
  'reopened',
] as const;
export type IncidentStatus = (typeof INCIDENT_STATUSES)[number];

export interface IncidentDTO {
  incident_id: string;
  title: string;
  description: string;
  severity: IncidentSeverity;
  status: IncidentStatus;
  source: 'manual' | 'alert';
  asset_id?: string;
  assignee_user_id?: string;
  row_version: number;
  created_at: string;
  updated_at: string;
  acknowledged_at?: string;
  resolved_at?: string;
  closed_at?: string;
  // root_incident_id/is_root project E4.2's topology-aware grouping link —
  // see toIncidentDTO's own doc comment (handlers_incidents.go). Absent
  // root_incident_id + is_root=true means "anchors its own group, or has no
  // group at all"; that ambiguity is resolved client-side by grouping.ts,
  // never assumed from this field alone.
  root_incident_id?: string;
  is_root: boolean;
}

export interface IncidentEventDTO {
  event_id: string;
  incident_id: string;
  kind: 'created' | 'status_transitioned' | 'assigned' | 'alert_note';
  field?: string;
  old_value?: string;
  new_value?: string;
  actor: string;
  row_version: number;
  occurred_at: string;
}

/**
 * The bound on a single board fetch. ADR-NOC-004: the board asks for every
 * open-class incident up to this cap in one request and groups root/
 * collateral client-side from that set — no keyset paging is chased, because
 * the server's list response carries no next-page cursor at all (unlike
 * /v1/artifacts' `next_cursor`). A tenant with more open incidents than this
 * cap can have a collateral incident whose root falls outside the fetched
 * page; groupIncidents.ts degrades that case to a flat (ungrouped) row rather
 * than dropping it or guessing.
 */
export const INCIDENT_LIST_CAP = 100;

export interface ListIncidentsOptions {
  /** Omitted or '' returns every status — the server's own default. */
  status?: IncidentStatus | '';
  limit?: number;
}

export function listIncidents(
  opts: ListIncidentsOptions = {},
  signal?: AbortSignal,
): Promise<{ items: IncidentDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(opts.limit ?? INCIDENT_LIST_CAP));
  if (opts.status) p.set('status', opts.status);
  return getJSON<{ items: IncidentDTO[] }>(`/v1/admin/incidents?${p.toString()}`, signal);
}

export function getIncident(incidentId: string, signal?: AbortSignal): Promise<IncidentDTO> {
  return getJSON<IncidentDTO>(`/v1/admin/incidents/${encodeURIComponent(incidentId)}`, signal);
}

export function getIncidentTimeline(
  incidentId: string,
  signal?: AbortSignal,
): Promise<{ items: IncidentEventDTO[] }> {
  return getJSON<{ items: IncidentEventDTO[] }>(
    `/v1/admin/incidents/${encodeURIComponent(incidentId)}/timeline`,
    signal,
  );
}

// ---------------------------------------------------------------------------
// Write actions (E-ACT.1, ADR-ACT-001). Every mutation below sends the
// `row_version` the caller last read back to the server (optimistic locking)
// and returns the server's own post-mutation incidentDTO — the caller is
// expected to treat that response, or a fresh GET, as the new truth rather
// than assuming its own request succeeded exactly as sent.

/**
 * The whole lifecycle, as data — a client-side mirror of
 * `domain.incidentTransitions` (internal/domain/incident.go). Kept in
 * lock-step with the Go table deliberately: this is the ONLY thing that gates
 * which transition buttons the console offers, so an entry here that drifts
 * from the server's own `CanTransitionTo` would either offer an illegal move
 * (caught late, as a 409) or hide a legal one. There is no runtime way to
 * derive this from the server today (no "legal next states" field on
 * incidentDTO), so it is restated here rather than fetched.
 */
export const INCIDENT_TRANSITIONS: Record<IncidentStatus, IncidentStatus[]> = {
  open: ['acknowledged'],
  acknowledged: ['investigating'],
  investigating: ['resolved'],
  resolved: ['closed', 'reopened'],
  reopened: ['investigating'],
  closed: [],
};

/** The legal next statuses from `status` — never anything `INCIDENT_TRANSITIONS` doesn't list. */
export function legalTransitions(status: IncidentStatus): IncidentStatus[] {
  return INCIDENT_TRANSITIONS[status];
}

export interface CreateIncidentInput {
  title: string;
  description?: string;
  severity: IncidentSeverity;
  asset_id?: string;
  assignee_user_id?: string;
}

/** POST /v1/admin/incidents — createIncidentRequest (handlers_incidents.go); always opens IncidentOpen. */
export function createIncident(input: CreateIncidentInput, signal?: AbortSignal): Promise<IncidentDTO> {
  return postJSON<IncidentDTO>('/v1/admin/incidents', input, signal);
}

/**
 * POST /v1/admin/incidents/{id}/transition — transitionIncidentRequest.
 * `rowVersion` MUST be the value last read for this incident; a stale one
 * returns 409 (handlers_incidents.go maps both `ErrVersionMismatch` and
 * `ErrInvalidTransition` to 409 for this endpoint specifically — unlike the
 * governance surface's 412 — so callers treat "someone else changed it" and
 * "this move is no longer legal" identically: refetch, never blind-retry).
 */
export function transitionIncident(
  incidentId: string,
  status: IncidentStatus,
  rowVersion: number,
  signal?: AbortSignal,
): Promise<IncidentDTO> {
  return postJSON<IncidentDTO>(
    `/v1/admin/incidents/${encodeURIComponent(incidentId)}/transition`,
    { row_version: rowVersion, status },
    signal,
  );
}

/**
 * POST /v1/admin/incidents/{id}/assign — assignIncidentRequest. `assigneeUserId`
 * null clears the assignment (the server distinguishes "clear" from "leave
 * unchanged" — there is no "leave unchanged" option on this endpoint at all,
 * see assignIncidentRequest's own doc comment).
 */
export function assignIncident(
  incidentId: string,
  assigneeUserId: string | null,
  rowVersion: number,
  signal?: AbortSignal,
): Promise<IncidentDTO> {
  return postJSON<IncidentDTO>(
    `/v1/admin/incidents/${encodeURIComponent(incidentId)}/assign`,
    { row_version: rowVersion, assignee_user_id: assigneeUserId },
    signal,
  );
}
