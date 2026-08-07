import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import type { AssetCriticality, VulnFindingPriority, VulnFindingSeverity, VulnFindingStatus } from './vulnerabilities';

// Shared rendering vocabulary for VulnerabilitiesPage + VulnFindingDetail —
// one severity/status/priority/criticality palette and rank, not
// independently maintained copies. `humanise`/`isOpenIncidentStatus` are
// reused directly from incidentPresentation.ts by callers rather than
// re-exported here — see IncidentBoardPage/AlertsBoardPage's own cross-file
// import of `humanise` for the established idiom.

/** Carries forward the fixed ADR-NOC-002/003 palette: red=high/critical, amber=medium, blue=low, grey=none (no vulnerability at all). */
export const VULN_SEVERITY_TYPE: Record<VulnFindingSeverity, StatusIndicatorProps.Type> = {
  none: 'stopped',
  low: 'info',
  medium: 'warning',
  high: 'error',
  critical: 'error',
};

/** Lower is more severe — mirrors incidentPresentation's SEVERITY_RANK, extended with `none` at the bottom. */
export const VULN_SEVERITY_RANK: Record<VulnFindingSeverity, number> = {
  critical: 0,
  high: 1,
  medium: 2,
  low: 3,
  none: 4,
};

export const VULN_STATUS_TYPE: Record<VulnFindingStatus, StatusIndicatorProps.Type> = {
  open: 'error',
  remediated: 'success',
  accepted_risk: 'warning',
  false_positive: 'stopped',
};

/** Priority (E8.3b's derived urgency label) reuses the same red/amber/blue reading as severity. */
export const VULN_PRIORITY_TYPE: Record<VulnFindingPriority, StatusIndicatorProps.Type> = {
  critical: 'error',
  high: 'error',
  medium: 'warning',
  low: 'info',
};

/** Lower is higher priority — the prioritized board's default read order. */
export const VULN_PRIORITY_RANK: Record<VulnFindingPriority, number> = {
  critical: 0,
  high: 1,
  medium: 2,
  low: 3,
};

export const ASSET_CRITICALITY_TYPE: Record<AssetCriticality, StatusIndicatorProps.Type> = {
  critical: 'error',
  high: 'error',
  medium: 'warning',
  low: 'info',
  unknown: 'stopped',
};

/** Lower is more critical — `unknown` ranks last, mirroring criticalityRank's own 1-based ordering (domain.VulnFindingRepository.Prioritized) without conflating rank value with sort direction. */
export const ASSET_CRITICALITY_RANK: Record<AssetCriticality, number> = {
  critical: 0,
  high: 1,
  medium: 2,
  low: 3,
  unknown: 4,
};
