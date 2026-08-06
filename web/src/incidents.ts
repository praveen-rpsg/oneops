import { getJSON } from './api';

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
