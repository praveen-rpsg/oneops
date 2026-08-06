import { deleteJSON, getJSON, patchJSON, postJSON } from './api';

// Typed contract for GET/POST/PATCH/DELETE /v1/admin/escalation-policies{,/
// {id},/{id}/tiers{,/{tierId},/reorder}}. Mirrors
// internal/httpapi/handlers_escalation.go's escalationPolicyDTO/
// escalationTierDTO exactly — field names and shape, not a paraphrase. Kept
// hand-written, like onCall.ts/incidents.ts, until a second consumer of the
// generated spec appears.
//
// Contract facts confirmed against the Go before E-ACT.5 was written
// (handlers_escalation.go, domain/escalation.go, server.go route table):
//
// - POST /v1/admin/escalation-policies (createEscalationPolicyRequest):
//   name only, required. policy_id is minted server-side; a freshly created
//   policy is always "active" — there is no caller-chosen initial status
//   (domain.NewEscalationPolicy).
// - PATCH /v1/admin/escalation-policies/{id} (patchEscalationPolicyRequest):
//   **requires** row_version (rejected < 1 as a 422); name/status are both
//   independently optional pointers, at least one required. A stale
//   row_version (ErrVersionMismatch) is 409.
// - DELETE /v1/admin/escalation-policies/{id} **exists** and, like
//   deleteOnCallSchedule before it (ADR-ACT-005 Decision 1), is a real route
//   (domain.EscalationPolicyRepository.Delete has no row_version parameter).
//   EscalationPolicy's own doc comment applies the identical
//   archive-don't-delete doctrine OnCallSchedule uses, so this module does
//   NOT export a deleteEscalationPolicy function — Edit's status field
//   ("active"/"archived") is the only retirement path, matching
//   ADR-ACT-005 Decision 1 exactly.
// - POST .../tiers (addEscalationTierRequest): on_call_schedule_id and
//   wait_seconds only; position is always "append at the end", never
//   caller-chosen. **No row_version anywhere in this request** — AddTier
//   takes no optimistic-lock parameter. 404 means on_call_schedule_id does
//   not name a schedule of the caller's tenant (or the policy itself does
//   not exist); 409 means the tier "could not be added" (domain.ErrConflict)
//   — a business-rule conflict, not a stale-read one, the same split
//   ADR-ACT-005 Decision 3 made for add-participant.
// - DELETE .../tiers/{tierId}: **no row_version, no body** — RemoveTier
//   takes no optimistic-lock parameter either.
// - POST .../tiers/reorder (reorderEscalationTiersRequest): tier_ids must
//   name the policy's full CURRENT tier set, in the desired new order — no
//   more, no fewer. **No row_version** — ReorderTiers takes no
//   optimistic-lock parameter. A mismatched set is refused with a 422
//   before anything is written; the rewrite is atomic (every position moves
//   or none do), the same E5.2b-1 deferred-position-unique guarantee
//   ADR-ACT-005 recorded for on-call participant reorder.
// - escalationTierDTO does carry its own row_version field (every DTO in
//   this package does), but none of Add/Remove/Reorder Tier currently
//   consume it — read-only here today, not a fabricated round-trip.

export const ESCALATION_POLICY_STATUSES = ['active', 'archived'] as const;
export type EscalationPolicyStatus = (typeof ESCALATION_POLICY_STATUSES)[number];

export interface EscalationPolicyDTO {
  policy_id: string;
  name: string;
  status: EscalationPolicyStatus;
  row_version: number;
  created_at: string;
  updated_at: string;
}

export interface EscalationTierDTO {
  tier_id: string;
  policy_id: string;
  position: number;
  on_call_schedule_id: string;
  wait_seconds: number;
  row_version: number;
  created_at: string;
  updated_at: string;
}

/** The bound on the policy list fetch — one page, matching every other board's list cap (onCall.ts' ON_CALL_SCHEDULE_LIST_CAP). */
export const ESCALATION_POLICY_LIST_CAP = 100;

export function listEscalationPolicies(signal?: AbortSignal): Promise<{ items: EscalationPolicyDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(ESCALATION_POLICY_LIST_CAP));
  return getJSON<{ items: EscalationPolicyDTO[] }>(`/v1/admin/escalation-policies?${p.toString()}`, signal);
}

export function getEscalationPolicy(policyId: string, signal?: AbortSignal): Promise<EscalationPolicyDTO> {
  return getJSON<EscalationPolicyDTO>(`/v1/admin/escalation-policies/${encodeURIComponent(policyId)}`, signal);
}

export function listEscalationTiers(
  policyId: string,
  signal?: AbortSignal,
): Promise<{ items: EscalationTierDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', '100');
  return getJSON<{ items: EscalationTierDTO[] }>(
    `/v1/admin/escalation-policies/${encodeURIComponent(policyId)}/tiers?${p.toString()}`,
    signal,
  );
}

export interface CreateEscalationPolicyInput {
  name: string;
}

/** POST /v1/admin/escalation-policies — createEscalationPolicyRequest. policy_id is minted server-side. */
export function createEscalationPolicy(
  input: CreateEscalationPolicyInput,
  signal?: AbortSignal,
): Promise<EscalationPolicyDTO> {
  return postJSON<EscalationPolicyDTO>('/v1/admin/escalation-policies', input, signal);
}

export interface EscalationPolicyPatchInput {
  name?: string;
  status?: EscalationPolicyStatus;
}

/**
 * PATCH /v1/admin/escalation-policies/{id} — patchEscalationPolicyRequest.
 * row_version is the value the last GET of this policy returned; a stale
 * value throws an ApiError with status 409 (see this module's top-of-file
 * contract note).
 */
export function patchEscalationPolicy(
  policyId: string,
  patch: EscalationPolicyPatchInput,
  rowVersion: number,
  signal?: AbortSignal,
): Promise<EscalationPolicyDTO> {
  return patchJSON<EscalationPolicyDTO>(
    `/v1/admin/escalation-policies/${encodeURIComponent(policyId)}`,
    { ...patch, row_version: rowVersion },
    signal,
  );
}

/**
 * POST .../tiers — addEscalationTierRequest. Appends a tier pointed at
 * onCallScheduleId at the end of the ladder. No row_version (see this
 * module's top-of-file contract note); a 404 means onCallScheduleId does
 * not name a schedule of this tenant (or the policy does not exist), a 409
 * means the tier could not be added (a business-rule conflict).
 */
export function addEscalationTier(
  policyId: string,
  onCallScheduleId: string,
  waitSeconds: number,
  signal?: AbortSignal,
): Promise<EscalationTierDTO> {
  return postJSON<EscalationTierDTO>(
    `/v1/admin/escalation-policies/${encodeURIComponent(policyId)}/tiers`,
    { on_call_schedule_id: onCallScheduleId, wait_seconds: waitSeconds },
    signal,
  );
}

/** DELETE .../tiers/{tierId}. No row_version, no body (see this module's top-of-file contract note). */
export function removeEscalationTier(policyId: string, tierId: string, signal?: AbortSignal): Promise<void> {
  return deleteJSON(
    `/v1/admin/escalation-policies/${encodeURIComponent(policyId)}/tiers/${encodeURIComponent(tierId)}`,
    signal,
  );
}

/**
 * POST .../tiers/reorder — reorderEscalationTiersRequest. tierIds must be
 * exactly the policy's current tier set, in the desired new order (this
 * module's top-of-file contract note); the caller
 * (components/EscalationPolicyDetail.tsx) always sends the full ladder it
 * just rendered, never a partial move, so the "atomic set, not N
 * interleaved moves" hard constraint holds by construction.
 */
export function reorderEscalationTiers(
  policyId: string,
  tierIds: string[],
  signal?: AbortSignal,
): Promise<{ items: EscalationTierDTO[] }> {
  return postJSON<{ items: EscalationTierDTO[] }>(
    `/v1/admin/escalation-policies/${encodeURIComponent(policyId)}/tiers/reorder`,
    { tier_ids: tierIds },
    signal,
  );
}

/** `domain.MaxEscalationPolicyNameLength`, restated for client-side validation. */
export const MAX_ESCALATION_POLICY_NAME_LENGTH = 200;

/** `undefined` when v is empty (not yet answered) or valid; an error string otherwise. */
export function escalationPolicyNameError(v: string): string | undefined {
  return v.length > MAX_ESCALATION_POLICY_NAME_LENGTH
    ? `Must be at most ${MAX_ESCALATION_POLICY_NAME_LENGTH} characters.`
    : undefined;
}

/** `domain.MinEscalationWaitSeconds`, restated: a tier must wait at least one second before advancing. */
export function escalationWaitSecondsError(seconds: number): string | undefined {
  return Number.isFinite(seconds) && seconds >= 1 ? undefined : 'Must be greater than 0.';
}

/**
 * Friendly wait-time presets offered before falling back to a raw "custom
 * seconds" field — the same "friendly units if easy" shape onCall.ts'
 * ON_CALL_HANDOFF_PRESETS uses for its own duration field.
 */
export const ESCALATION_WAIT_PRESETS: { value: string; label: string; seconds?: number }[] = [
  { value: '300', label: '5 minutes', seconds: 300 },
  { value: '900', label: '15 minutes', seconds: 900 },
  { value: '1800', label: '30 minutes', seconds: 1_800 },
  { value: 'custom', label: 'Custom (seconds)' },
];

/** The preset matching a given wait time, or 'custom' if it matches none of them. */
export function presetForWaitSeconds(seconds: number): string {
  const match = ESCALATION_WAIT_PRESETS.find((p) => p.seconds === seconds);
  return match ? match.value : 'custom';
}
