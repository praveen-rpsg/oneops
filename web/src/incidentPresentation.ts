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
