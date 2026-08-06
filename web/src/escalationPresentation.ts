import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import type { EscalationPolicyStatus } from './escalation';

// Rendering vocabulary for the escalation board (routes/EscalationBoardPage.tsx).
// Duration formatting for wait_seconds reuses onCallPresentation.ts'
// formatDurationSeconds directly (same shape, no need to reimplement).

export const ESCALATION_POLICY_STATUS_TYPE: Record<EscalationPolicyStatus, StatusIndicatorProps.Type> = {
  active: 'success',
  archived: 'stopped',
};
