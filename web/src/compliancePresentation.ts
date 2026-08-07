import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import type { ComplianceControlStatus } from './complianceControls';

// Shared rendering vocabulary for CompliancePage + ComplianceControlDetail —
// one status palette and rank, not independently maintained copies.
// `humanise` is reused directly from incidentPresentation.ts, the same
// cross-file idiom vulnFindingPresentation.ts/riskPresentation.ts already
// establish.

/** Mirrors domain.ComplianceControlStatus's own lifecycle reading (internal/domain/compliance_control.go:48-70): not-yet-done reads as attention-needed, in progress is neutral-active, implemented is the goal state, not_applicable is a deliberate exclusion (not a failure). */
export const COMPLIANCE_CONTROL_STATUS_TYPE: Record<ComplianceControlStatus, StatusIndicatorProps.Type> = {
  not_implemented: 'error',
  in_progress: 'in-progress',
  implemented: 'success',
  not_applicable: 'stopped',
};

/** Board default read order: not_implemented first (most attention needed), not_applicable last. */
export const COMPLIANCE_CONTROL_STATUS_RANK: Record<ComplianceControlStatus, number> = {
  not_implemented: 0,
  in_progress: 1,
  implemented: 2,
  not_applicable: 3,
};
