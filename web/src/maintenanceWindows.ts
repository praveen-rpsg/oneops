import { deleteJSON, getJSON, postJSON } from './api';

// Typed contract for GET/POST/DELETE /v1/admin/maintenance-windows{,/{id}}.
// Mirrors internal/httpapi/handlers_maintenance_window.go's
// maintenanceWindowDTO exactly — field names and shape, not a paraphrase.
// Confirmed against the Go before this module was written (E-ACT.3):
//
// - GET /v1/admin/maintenance-windows?limit=&after= — keyset-paginated over
//   window_id (postgres.MaintenanceWindowStore.List), returns `{items:[...]}`
//   with no next-page cursor in the body, the same "one bounded page" shape
//   listAlertRules/listOnCallSchedules already use.
// - GET /v1/admin/maintenance-windows/{id} — one window; not called by this
//   console (the list already returns full DTOs and this story has no
//   drill-down surface — see routes/MaintenanceBoardPage.tsx's doc comment).
// - POST /v1/admin/maintenance-windows (createMaintenanceWindowRequest):
//   asset_id/starts_at/ends_at/reason. window_id is minted server-side;
//   created_by/suppressed_count/last_suppressed_at are repository/
//   evaluator-owned and never accepted from the caller.
//   domain.MaintenanceWindow.Validate rejects ends_at <= starts_at (the
//   window is half-open [starts_at, ends_at) — ADR-ALERTING-002 §3).
// - DELETE /v1/admin/maintenance-windows/{id} — cancels/withdraws a window.
//   CONFIRMED CONTRACT FACT, not assumed: takes no request body and no
//   row_version at all (domain.MaintenanceWindowRepository.Delete(ctx,
//   windowID) has no optimistic-lock parameter), the same asymmetry
//   ADR-ACT-002 already found and recorded for deleteAlertRule.

export interface MaintenanceWindowDTO {
  window_id: string;
  asset_id: string;
  starts_at: string;
  ends_at: string;
  reason: string;
  created_by: string;
  suppressed_count: number;
  last_suppressed_at?: string;
  row_version: number;
  created_at: string;
  updated_at: string;
}

/**
 * The bound on a single board fetch — the same "one page, no keyset chasing"
 * posture alertRules.ts' ALERT_RULE_LIST_CAP documents, for the same reason:
 * this list endpoint carries no next-page cursor in its response body.
 */
export const MAINTENANCE_WINDOW_LIST_CAP = 100;

export interface ListMaintenanceWindowsOptions {
  limit?: number;
}

export function listMaintenanceWindows(
  opts: ListMaintenanceWindowsOptions = {},
  signal?: AbortSignal,
): Promise<{ items: MaintenanceWindowDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(opts.limit ?? MAINTENANCE_WINDOW_LIST_CAP));
  return getJSON<{ items: MaintenanceWindowDTO[] }>(`/v1/admin/maintenance-windows?${p.toString()}`, signal);
}

/** `domain.MaxMaintenanceWindowReasonLength`, restated for client-side validation. */
export const MAINTENANCE_WINDOW_REASON_MAX_LENGTH = 500;

/** `undefined` when v is empty (not yet answered) or valid; an error string otherwise. */
export function maintenanceWindowAssetIdError(v: string): string | undefined {
  return v.length > 0 && v.trim().length === 0 ? 'Asset ID is required.' : undefined;
}

export function maintenanceWindowReasonError(v: string): string | undefined {
  return v.length > MAINTENANCE_WINDOW_REASON_MAX_LENGTH
    ? `Must be at most ${MAINTENANCE_WINDOW_REASON_MAX_LENGTH} characters.`
    : undefined;
}

/**
 * `domain.MaintenanceWindow.Validate`'s half-open-interval rule, restated:
 * ends must be strictly after starts. `undefined` when either bound is
 * missing (not yet answered) or the range is valid.
 */
export function maintenanceWindowRangeError(startsAt?: Date, endsAt?: Date): string | undefined {
  if (!startsAt || !endsAt) return undefined;
  if (Number.isNaN(startsAt.getTime()) || Number.isNaN(endsAt.getTime())) return undefined;
  return endsAt.getTime() > startsAt.getTime()
    ? undefined
    : 'End must be strictly after start — the window is half-open [start, end).';
}

/**
 * Combines a Cloudscape DateInput value ("YYYY-MM-DD") and TimeInput value
 * ("HH:mm") into a local-time `Date`, or `undefined` if either half is
 * incomplete/invalid. The pair is read as the operator's own wall-clock time
 * (browser-local); `.toISOString()` on the result is what actually goes over
 * the wire, which is what converts it to the RFC3339 UTC `starts_at`/
 * `ends_at` the server expects.
 */
export function combineDateAndTime(date: string, time: string): Date | undefined {
  if (!date || !time) return undefined;
  const d = new Date(`${date}T${time}:00`);
  return Number.isNaN(d.getTime()) ? undefined : d;
}

export type MaintenanceWindowStatus = 'upcoming' | 'active' | 'expired';

/**
 * Active/upcoming/expired is not a server-side field — it is derived purely
 * from `now` against the window's own half-open [starts_at, ends_at) bounds
 * (ADR-ALERTING-002 §3): `now === starts_at` is active, `now === ends_at` is
 * already expired.
 */
export function computeMaintenanceWindowStatus(
  win: Pick<MaintenanceWindowDTO, 'starts_at' | 'ends_at'>,
  now: Date = new Date(),
): MaintenanceWindowStatus {
  const t = now.getTime();
  const starts = new Date(win.starts_at).getTime();
  const ends = new Date(win.ends_at).getTime();
  if (t < starts) return 'upcoming';
  if (t >= ends) return 'expired';
  return 'active';
}

export interface CreateMaintenanceWindowInput {
  asset_id: string;
  starts_at: string;
  ends_at: string;
  reason?: string;
}

/**
 * POST /v1/admin/maintenance-windows — createMaintenanceWindowRequest
 * (handlers_maintenance_window.go). `asset_id` must already name a
 * Configuration Item visible to the caller's tenant, re-verified
 * server-side (404 otherwise). `window_id` is minted server-side and never
 * sent.
 */
export function createMaintenanceWindow(
  input: CreateMaintenanceWindowInput,
  signal?: AbortSignal,
): Promise<MaintenanceWindowDTO> {
  return postJSON<MaintenanceWindowDTO>('/v1/admin/maintenance-windows', input, signal);
}

/**
 * DELETE /v1/admin/maintenance-windows/{id} — cancels a window.
 *
 * CONFIRMED CONTRACT FACT (this story), not assumed: this endpoint takes
 * **no `row_version` and no request body at all** —
 * `deleteMaintenanceWindow`'s Go handler
 * (`internal/httpapi/handlers_maintenance_window.go`) calls
 * `s.maintenanceWindows.Delete(ctx, id)`, which has no optimistic-lock
 * parameter in `domain.MaintenanceWindowRepository` either — the same
 * asymmetry `deleteAlertRule` has (ADR-ACT-002 §1). There is therefore no
 * way for this call to detect "the window changed since the board loaded"
 * the way a `row_version`-guarded PATCH can.
 */
export function deleteMaintenanceWindow(windowId: string, signal?: AbortSignal): Promise<void> {
  return deleteJSON(`/v1/admin/maintenance-windows/${encodeURIComponent(windowId)}`, signal);
}
