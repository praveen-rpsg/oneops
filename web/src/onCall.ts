import { deleteJSON, getJSON, patchJSON, postJSON } from './api';

// Typed contract for GET/POST/PATCH/DELETE /v1/admin/on-call-schedules{,/{id},
// /{id}/on-call,/{id}/participants{,/{participantId},/reorder}}. Mirrors
// internal/httpapi/handlers_oncall.go's onCallScheduleDTO/onCallParticipantDTO/
// onCallNowDTO exactly — field names and shape, not a paraphrase. Kept
// hand-written, like incidents.ts/noc.ts, until a second consumer of the
// generated spec appears.
//
// Contract facts confirmed against the Go before E-ACT.4 was written
// (handlers_oncall.go, domain/oncall.go):
//
// - POST /v1/admin/on-call-schedules (createOnCallScheduleRequest): name/
//   handoff_interval_seconds/rotation_start_at, all required.
//   schedule_id is minted server-side; status is always "active" at creation
//   (there is no caller-chosen initial status).
// - PATCH /v1/admin/on-call-schedules/{id} (patchOnCallScheduleRequest):
//   **requires** row_version (rejected < 1 as a 422); name/
//   handoff_interval_seconds/rotation_start_at/status are all independently
//   optional pointers — at least one must be set. Stale row_version is 409.
// - DELETE /v1/admin/on-call-schedules/{id} exists
//   (domain.OnCallScheduleRepository.Delete has no row_version parameter,
//   the same asymmetry ADR-ACT-002/ADR-ACT-004 already found for
//   deleteAlertRule/deleteMaintenanceWindow) but is NOT wired into this
//   console: OnCallSchedule's own doc comment states a schedule "is a
//   governed, named object an operator archives rather than deletes" —
//   `status: "archived"` via PATCH is the intended retirement path, not
//   DELETE. No deleteOnCallSchedule function exists here by design.
// - POST .../participants (addOnCallParticipantRequest): user_id only;
//   position is always "append at the end", never caller-chosen.
//   **No row_version anywhere in this request** — AddParticipant takes no
//   optimistic-lock parameter. 404 means user_id does not name an ACTIVE
//   member of the caller's tenant; 409 means user_id is already on this
//   roster (a business-rule conflict, not a stale-read conflict — the
//   response detail says so directly, and this console surfaces it as an
//   ordinary inline error rather than the row_version "changed since you
//   loaded it" refetch banner ADR-ACT-001 established for schedule PATCH).
// - DELETE .../participants/{participantId}: **no row_version, no body** —
//   RemoveParticipant takes no optimistic-lock parameter either.
// - POST .../participants/reorder (reorderOnCallParticipantsRequest):
//   participant_ids names the schedule's FULL current participant set, in
//   the desired new order — no more, no fewer. **No row_version** —
//   ReorderParticipants takes no optimistic-lock parameter. A mismatched set
//   is refused with a 422 before anything is written (atomic: either every
//   position moves or none do).
//
// onCallParticipantDTO does carry its own row_version field (every DTO in
// this package does), but none of Add/Remove/Reorder Participant currently
// consume it — it is read-only here today, not a fabricated round-trip.

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

export function getOnCallSchedule(scheduleId: string, signal?: AbortSignal): Promise<OnCallScheduleDTO> {
  return getJSON<OnCallScheduleDTO>(`/v1/admin/on-call-schedules/${encodeURIComponent(scheduleId)}`, signal);
}

export interface CreateOnCallScheduleInput {
  name: string;
  handoff_interval_seconds: number;
  rotation_start_at: string;
}

/** POST /v1/admin/on-call-schedules — createOnCallScheduleRequest. schedule_id is minted server-side. */
export function createOnCallSchedule(
  input: CreateOnCallScheduleInput,
  signal?: AbortSignal,
): Promise<OnCallScheduleDTO> {
  return postJSON<OnCallScheduleDTO>('/v1/admin/on-call-schedules', input, signal);
}

export interface OnCallSchedulePatchInput {
  name?: string;
  handoff_interval_seconds?: number;
  rotation_start_at?: string;
  status?: OnCallScheduleStatus;
}

/**
 * PATCH /v1/admin/on-call-schedules/{id} — patchOnCallScheduleRequest.
 * row_version is the value the last GET of this schedule returned; a stale
 * value throws an ApiError with status 409 (see this module's top-of-file
 * contract note).
 */
export function patchOnCallSchedule(
  scheduleId: string,
  patch: OnCallSchedulePatchInput,
  rowVersion: number,
  signal?: AbortSignal,
): Promise<OnCallScheduleDTO> {
  return patchJSON<OnCallScheduleDTO>(
    `/v1/admin/on-call-schedules/${encodeURIComponent(scheduleId)}`,
    { ...patch, row_version: rowVersion },
    signal,
  );
}

/**
 * POST .../participants — addOnCallParticipantRequest. Appends userId at the
 * end of the rotation. No row_version (see this module's top-of-file
 * contract note); a 404 means userId does not name an ACTIVE member of this
 * tenant, a 409 means userId is already on this roster.
 */
export function addOnCallParticipant(
  scheduleId: string,
  userId: string,
  signal?: AbortSignal,
): Promise<OnCallParticipantDTO> {
  return postJSON<OnCallParticipantDTO>(
    `/v1/admin/on-call-schedules/${encodeURIComponent(scheduleId)}/participants`,
    { user_id: userId },
    signal,
  );
}

/** DELETE .../participants/{participantId}. No row_version, no body (see this module's top-of-file contract note). */
export function removeOnCallParticipant(
  scheduleId: string,
  participantId: string,
  signal?: AbortSignal,
): Promise<void> {
  return deleteJSON(
    `/v1/admin/on-call-schedules/${encodeURIComponent(scheduleId)}/participants/${encodeURIComponent(participantId)}`,
    signal,
  );
}

/**
 * POST .../participants/reorder — reorderOnCallParticipantsRequest.
 * participantIds must be exactly the schedule's current participant set, in
 * the desired new order (this module's top-of-file contract note); the
 * caller (components/OnCallScheduleDetail.tsx) always sends the full roster
 * it just rendered, never a partial move, so the "atomic set, not N
 * interleaved moves" hard constraint holds by construction.
 */
export function reorderOnCallParticipants(
  scheduleId: string,
  participantIds: string[],
  signal?: AbortSignal,
): Promise<{ items: OnCallParticipantDTO[] }> {
  return postJSON<{ items: OnCallParticipantDTO[] }>(
    `/v1/admin/on-call-schedules/${encodeURIComponent(scheduleId)}/participants/reorder`,
    { participant_ids: participantIds },
    signal,
  );
}

/** `domain.MaxOnCallScheduleNameLength`, restated for client-side validation. */
export const MAX_ON_CALL_SCHEDULE_NAME_LENGTH = 200;

/** `undefined` when v is empty (not yet answered) or valid; an error string otherwise. */
export function onCallScheduleNameError(v: string): string | undefined {
  return v.length > MAX_ON_CALL_SCHEDULE_NAME_LENGTH
    ? `Must be at most ${MAX_ON_CALL_SCHEDULE_NAME_LENGTH} characters.`
    : undefined;
}

/** `domain.MinOnCallHandoffIntervalSeconds`, restated: a rotation must hand off at least once a second. */
export function onCallHandoffIntervalError(seconds: number): string | undefined {
  return Number.isFinite(seconds) && seconds >= 1 ? undefined : 'Must be greater than 0.';
}

/**
 * Friendly handoff-interval presets offered before falling back to a raw
 * "custom seconds" field — the brief's own suggestion (1 day/1 week), so an
 * operator declaring an ordinary rotation never has to do the seconds
 * arithmetic themselves.
 */
export const ON_CALL_HANDOFF_PRESETS: { value: string; label: string; seconds?: number }[] = [
  { value: '86400', label: '1 day', seconds: 86_400 },
  { value: '604800', label: '1 week', seconds: 604_800 },
  { value: 'custom', label: 'Custom (seconds)' },
];

/** The preset matching an existing schedule's interval, or 'custom' if it matches none of them. */
export function presetForHandoffSeconds(seconds: number): string {
  const match = ON_CALL_HANDOFF_PRESETS.find((p) => p.seconds === seconds);
  return match ? match.value : 'custom';
}
