import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import type { ObservationSeverity, SecurityRuleState } from './securityRules';

// Rendering vocabulary for the detection-rules board (routes/DetectionRulesPage.tsx)
// and its detail panel (components/SecurityRuleDetail.tsx). incident_severity
// reuses incidentPresentation's SEVERITY_TYPE/SEVERITY_RANK directly (both are
// the same IncidentSeverity vocabulary — see securityRules.ts' own re-export
// doc comment); min_severity is ObservationSeverity, a distinct five-level
// scale with no "high"/"low"-only split, so it gets its own palette here.

/** Carries forward the fixed ADR-NOC-002/003 reading, extended with `info` at the calm end. */
export const OBSERVATION_SEVERITY_TYPE: Record<ObservationSeverity, StatusIndicatorProps.Type> = {
  critical: 'error',
  high: 'error',
  medium: 'warning',
  low: 'info',
  info: 'info',
};

/** Lower is more severe — matches `domain.DefaultSecurityRuleMinSeverity` ("info") sitting at the least-restrictive end. */
export const OBSERVATION_SEVERITY_RANK: Record<ObservationSeverity, number> = {
  critical: 0,
  high: 1,
  medium: 2,
  low: 3,
  info: 4,
};

/** firing is the "needs attention" state — red, the same reading ALERT_STATE_TYPE.firing gets. */
export const SECURITY_RULE_STATE_TYPE: Record<SecurityRuleState, StatusIndicatorProps.Type> = {
  firing: 'error',
  ok: 'success',
};

export const SECURITY_RULE_STATE_RANK: Record<SecurityRuleState, number> = {
  firing: 0,
  ok: 1,
};
