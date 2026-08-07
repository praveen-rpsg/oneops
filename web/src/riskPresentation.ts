import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import type { RiskImpact, RiskLikelihood, RiskSeverityBand, RiskStatus } from './risks';

// Shared rendering vocabulary for RiskRegisterPage + RiskDetail — one
// status/band/likelihood/impact palette and rank, not independently
// maintained copies. `humanise` is reused directly from
// incidentPresentation.ts, the same cross-file idiom
// vulnFindingPresentation.ts already establishes.

/** Lifecycle order (domain.RiskStatus's own progression — see internal/domain/risk.go:173-178's riskTransitions table). */
export const RISK_STATUS_TYPE: Record<RiskStatus, StatusIndicatorProps.Type> = {
  open: 'error',
  mitigating: 'in-progress',
  accepted: 'warning',
  closed: 'stopped',
};

/** Board default read order: open first, closed last. */
export const RISK_STATUS_RANK: Record<RiskStatus, number> = {
  open: 0,
  mitigating: 1,
  accepted: 2,
  closed: 3,
};

/** domain.RiskLikelihood.Rank's own 1-based table (internal/domain/risk.go:44-59) — used for column sort, not for color. */
export const RISK_LIKELIHOOD_RANK: Record<RiskLikelihood, number> = {
  rare: 1,
  unlikely: 2,
  possible: 3,
  likely: 4,
  almost_certain: 5,
};

/** domain.RiskImpact.Rank's own 1-based table (internal/domain/risk.go:87-102) — used for column sort, not for color. */
export const RISK_IMPACT_RANK: Record<RiskImpact, number> = {
  negligible: 1,
  minor: 2,
  moderate: 3,
  major: 4,
  severe: 5,
};

/** Carries forward the fixed ADR-NOC-002/003 palette: red=critical/high, amber=medium, blue=low — the same reading VULN_PRIORITY_TYPE already gives its own four-band scale. */
export const RISK_SEVERITY_BAND_TYPE: Record<RiskSeverityBand, StatusIndicatorProps.Type> = {
  critical: 'error',
  high: 'error',
  medium: 'warning',
  low: 'info',
};

/** Lower is more severe — the register view's default read order (it arrives server-ranked already; this is for client re-sort only). */
export const RISK_SEVERITY_BAND_RANK: Record<RiskSeverityBand, number> = {
  critical: 0,
  high: 1,
  medium: 2,
  low: 3,
};
