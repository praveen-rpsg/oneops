import { getJSON } from './api';

// Typed contract for GET /v1/admin/on-call-schedules{,/{id},/{id}/on-call,
// /{id}/participants}. Mirrors internal/httpapi/handlers_oncall.go's
// onCallScheduleDTO/onCallParticipantDTO/onCallNowDTO exactly — field names
// and shape, not a paraphrase. Kept hand-written, like incidents.ts/noc.ts,
// until a second consumer of the generated spec appears.

export const ON_CALL_SCHEDULE_STATUSES = ['active', 'archived'] as const;
export type OnCallScheduleStatus = (typeof ON_CALL_SCHEDULE_STATUSES)[number];

export interface OnCallScheduleDTO {
  schedule_id: string;
  name: string;
  handoff_interval_seconds: number;
  rotation_start_at: string;
  status: OnCallScheduleStatus;
  row_version: number;
  created_at: string;
  updated_at: string;
}

export interface OnCallParticipantDTO {
  participant_id: string;
  schedule_id: string;
  user_id: string;
  position: number;
  row_version: number;
  created_at: string;
  updated_at: string;
}

/** user_id/display_name are both absent when the schedule has no participants — an ordinary, non-error state, not a 404. */
export interface OnCallNowDTO {
  schedule_id: string;
  at: string;
  user_id?: string;
  display_name?: string;
}

/** The bound on the schedule list fetch — one page, matching every other board's list cap (incidents.ts' INCIDENT_LIST_CAP, alertRules.ts' ALERT_RULE_LIST_CAP). */
export const ON_CALL_SCHEDULE_LIST_CAP = 100;

/**
 * The bound on the per-schedule supplementary fetches this board issues
 * after the schedule list loads (`/on-call` and `/participants`, two
 * requests per schedule, run in parallel via Promise.allSettled) — an N+1
 * pattern like api.ts's `getRelations`/`RESOLVE_LIMIT`, capped smaller here
 * because it is two round trips per schedule rather than one. A schedule
 * beyond this cap still renders (name, handoff interval) with an explicit
 * "not loaded" state for on-call/participants rather than the board issuing
 * an unbounded number of parallel requests.
 */
export const ON_CALL_DETAIL_FETCH_CAP = 20;

export function listOnCallSchedules(
  signal?: AbortSignal,
): Promise<{ items: OnCallScheduleDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(ON_CALL_SCHEDULE_LIST_CAP));
  return getJSON<{ items: OnCallScheduleDTO[] }>(`/v1/admin/on-call-schedules?${p.toString()}`, signal);
}

export function getOnCallNow(scheduleId: string, signal?: AbortSignal): Promise<OnCallNowDTO> {
  return getJSON<OnCallNowDTO>(
    `/v1/admin/on-call-schedules/${encodeURIComponent(scheduleId)}/on-call`,
    signal,
  );
}

export function listOnCallParticipants(
  scheduleId: string,
  signal?: AbortSignal,
): Promise<{ items: OnCallParticipantDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', '100');
  return getJSON<{ items: OnCallParticipantDTO[] }>(
    `/v1/admin/on-call-schedules/${encodeURIComponent(scheduleId)}/participants?${p.toString()}`,
    signal,
  );
}
