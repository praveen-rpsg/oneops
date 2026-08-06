import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import type { OnCallScheduleStatus } from './onCall';

// Rendering vocabulary for the on-call board (routes/OnCallBoardPage.tsx).

export const ON_CALL_STATUS_TYPE: Record<OnCallScheduleStatus, StatusIndicatorProps.Type> = {
  active: 'success',
  archived: 'stopped',
};

/** A short, human-scale duration from seconds — "45m", "12h", "3d". Mirrors incidentPresentation.ts' ageLabel formatting choices. */
export function formatDurationSeconds(totalSeconds: number): string {
  if (totalSeconds < 60) return `${totalSeconds}s`;
  const mins = Math.floor(totalSeconds / 60);
  if (mins < 60) return `${mins}m`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h${mins % 60 ? ` ${mins % 60}m` : ''}`;
  const days = Math.floor(hours / 24);
  return `${days}d${hours % 24 ? ` ${hours % 24}h` : ''}`;
}
