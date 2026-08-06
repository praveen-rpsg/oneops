import { getJSON } from './api';

// Typed contract for GET /v1/admin/dashboards/incident-trends (E9.1,
// ADR-NOC-008). Mirrors internal/httpapi/handlers_dashboards.go's
// incidentTrendsResponseDTO exactly — field names and shape, not a
// paraphrase. Kept hand-written, like noc.ts/incidents.ts, until a second
// consumer of the generated spec appears.

export type IncidentTrendBucket = 'hour' | 'day';

export interface IncidentTrendSeverityCounts {
  critical: number;
  high: number;
  medium: number;
  low: number;
}

/**
 * `alert` is an incident-*source* proxy (ADR-NOC-008 §5), NOT a true
 * alert-firing log — there is no `alert_event` table in this system. It
 * counts incidents `alerting.IncidentCorrelator` created or linked off a
 * rule's firing, never how many times a rule actually fired.
 */
export interface IncidentTrendSourceCounts {
  manual: number;
  alert: number;
}

export interface IncidentTrendPoint {
  bucket_start: string;
  opened_total: number;
  opened_by_severity: IncidentTrendSeverityCounts;
  opened_by_source: IncidentTrendSourceCounts;
  resolved_total: number;
}

export interface IncidentTrendsResponse {
  from: string;
  to: string;
  bucket: IncidentTrendBucket;
  points: IncidentTrendPoint[];
}

/** Mirrors domain.MaxIncidentTrendBuckets — the server refuses anything wider (422) before this is ever called with a bad window. */
export function getIncidentTrends(
  from: Date,
  to: Date,
  bucket: IncidentTrendBucket,
  signal?: AbortSignal,
): Promise<IncidentTrendsResponse> {
  const p = new URLSearchParams({ from: from.toISOString(), to: to.toISOString(), bucket });
  return getJSON<IncidentTrendsResponse>(`/v1/admin/dashboards/incident-trends?${p.toString()}`, signal);
}
