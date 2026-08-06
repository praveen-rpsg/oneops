import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import type { AlertComparator, AlertRuleState, AlertSeverity } from './alertRules';

// Rendering vocabulary for the alerts board (routes/AlertsBoardPage.tsx) and
// its detail panel (components/AlertRuleDetail.tsx) — carries forward the
// same fixed palette ADR-NOC-002/003 established for alert severity
// (critical/high=error, warning/medium=warning, info/low=info), applied here
// to AlertRule's own three-value severity enum (no "high"/"low" — that
// distinction belongs to Incident.severity, a different enum).

export const ALERT_SEVERITY_TYPE: Record<AlertSeverity, StatusIndicatorProps.Type> = {
  critical: 'error',
  warning: 'warning',
  info: 'info',
};

/** Lower is more severe — the board's default sort order. */
export const ALERT_SEVERITY_RANK: Record<AlertSeverity, number> = {
  critical: 0,
  warning: 1,
  info: 2,
};

/** firing is the "needs attention" state — red, the same reading STATUS_TYPE.open gets on the incident board. */
export const ALERT_STATE_TYPE: Record<AlertRuleState, StatusIndicatorProps.Type> = {
  firing: 'error',
  ok: 'success',
};

export const ALERT_STATE_RANK: Record<AlertRuleState, number> = {
  firing: 0,
  ok: 1,
};

/** The relational symbol a comparator reads as, e.g. "> 90". */
export const COMPARATOR_SYMBOL: Record<AlertComparator, string> = {
  gt: '>',
  lt: '<',
  gte: '>=',
  lte: '<=',
};
