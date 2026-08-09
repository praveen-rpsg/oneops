import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import type { IncidentSeverity, IncidentStatus } from './incidents';

// Shared rendering vocabulary for both the board (IncidentBoardPage) and the
// drill-down (components/IncidentDetail.tsx) — one severity/status palette
// and rank, not two independently maintained copies.

export const humanise = (v: string) => v.replace(/_/g, ' ').replace(/^\w/, (c) => c.toUpperCase());

/** Carries forward the fixed palette ADR-NOC-002/003 already established: red=critical/high, amber=medium, blue=low. */
export const SEVERITY_TYPE: Record<IncidentSeverity, StatusIndicatorProps.Type> = {
  critical: 'error',
  high: 'error',
  medium: 'warning',
  low: 'info',
};

/** Lower is more severe — the board's default sort order. */
export const SEVERITY_RANK: Record<IncidentSeverity, number> = {
  critical: 0,
  high: 1,
  medium: 2,
  low: 3,
};

/**
 * Fixed hex colors for the severity series in the dashboards incident-volume
 * chart (E9.2). Cloudscape chart series take a literal `color` hex value,
 * not a `StatusIndicator` `type` — there is no separately installed
 * `@cloudscape-design/design-tokens` package to source themed custom
 * properties from (the same constraint ADR-NOC-007 already states for the
 * topology map), so this is a second fixed, hand-written palette rather than
 * a token lookup. Kept mode-agnostic on purpose: AWS's own qualitative chart
 * palettes are chosen to read on both a light and a dark chart background,
 * unlike topologyPresentation's SVG fills which sit directly on the page.
 * Carries forward the same red/red/amber/blue reading SEVERITY_TYPE already
 * establishes, with "high" one step lighter than "critical" so the two are
 * visually distinguishable in a chart legend (StatusIndicator has no such
 * need since it also renders the text label).
 */
export const SEVERITY_CHART_COLOR: Record<IncidentSeverity, string> = {
  critical: '#d13212',
  high: '#eb5f07',
  medium: '#8d6605',
  low: '#0972d3',
};

/** The resolved-incidents line series' color — green, distinct from every severity color above. */
export const RESOLVED_CHART_COLOR = '#037f51';

/**
 * Fixed hex colors for the incident-source series in the dashboards
 * by-source chart (E9.3, ADR-NOC-008 §5) — all four of
 * domain.IncidentSource's closed vocabulary. Same "fixed hex, no design-
 * tokens package" constraint SEVERITY_CHART_COLOR's own doc comment states.
 * Distinct from SEVERITY_CHART_COLOR's palette so the two charts are never
 * confused for the same legend.
 */
export const SOURCE_CHART_COLOR: Record<'manual' | 'alert' | 'security' | 'vuln', string> = {
  manual: '#545b64',
  alert: '#7d8998',
  security: '#8d6605',
  vuln: '#0972d3',
};

export const STATUS_TYPE: Record<IncidentStatus, StatusIndicatorProps.Type> = {
  open: 'error',
  acknowledged: 'warning',
  investigating: 'in-progress',
  resolved: 'success',
  closed: 'stopped',
  reopened: 'warning',
};

/** Lifecycle order (domain.IncidentStatus.CanTransitionTo's own progression, reopened re-enters after resolved). */
export const STATUS_RANK: Record<IncidentStatus, number> = {
  open: 0,
  acknowledged: 1,
  investigating: 2,
  reopened: 3,
  resolved: 4,
  closed: 5,
};

/**
 * "Open-class" for overlay purposes (topology map, E7.3b-2): every status
 * before the terminal resolved/closed pair in STATUS_RANK's own ordering.
 * Reused instead of re-deriving the split elsewhere.
 */
export function isOpenIncidentStatus(status: IncidentStatus): boolean {
  return STATUS_RANK[status] < STATUS_RANK.resolved;
}

/** A short, human-scale age — "<1m", "12m", "3h 4m", "2d 6h". */
export function ageLabel(createdAtIso: string): string {
  const ms = Date.now() - new Date(createdAtIso).getTime();
  const mins = Math.floor(ms / 60_000);
  if (mins < 1) return '<1m';
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ${mins % 60}m`;
  const days = Math.floor(hours / 24);
  return `${days}d ${hours % 24}h`;
}
