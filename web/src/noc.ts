import { getJSON } from './api';

// Typed contract for GET /v1/admin/noc/overview.
// Mirrors internal/httpapi/handlers_noc.go's nocOverviewDTO exactly — field
// names and shape, not a paraphrase. Kept hand-written, like api.ts, until a
// second consumer of the generated spec appears.

export interface NOCIncidentStatusCounts {
  open: number;
  acknowledged: number;
  investigating: number;
}

export interface NOCIncidentSeverityCounts {
  critical: number;
  high: number;
  medium: number;
  low: number;
}

export interface NOCIncidentGrouping {
  root_count: number;
  collateral_count: number;
}

export interface NOCIncidents {
  open_total: number;
  by_status: NOCIncidentStatusCounts;
  by_severity: NOCIncidentSeverityCounts;
  grouped: NOCIncidentGrouping;
}

export interface NOCAlertSeverityCounts {
  critical: number;
  warning: number;
  info: number;
}

export interface NOCAlerts {
  firing_total: number;
  by_severity: NOCAlertSeverityCounts;
}

export interface NOCAssets {
  stale: number;
  orphaned_assets: number;
  orphaned_business_services: number;
  incomplete: number;
}

/** `user_id`/`display_name` are both null when the schedule's roster is empty — not an error. */
export interface NOCOnCallEntry {
  schedule_id: string;
  schedule_name: string;
  user_id: string | null;
  display_name: string | null;
}

export interface NOCEscalations {
  active_total: number;
}

export interface NOCOverview {
  incidents: NOCIncidents;
  alerts: NOCAlerts;
  assets: NOCAssets;
  on_call: NOCOnCallEntry[];
  escalations: NOCEscalations;
  generated_at: string;
}

/** Fetches the current operational state. Throws ApiError carrying the server's problem+json. */
export function getNOCOverview(signal?: AbortSignal): Promise<NOCOverview> {
  return getJSON<NOCOverview>('/v1/admin/noc/overview', signal);
}
