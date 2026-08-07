import { getJSON, patchJSON, postJSON } from './api';

// Typed contract for GET/POST/PATCH /v1/admin/users{,/{id}} — the GLOBAL user
// registry (E-ID.3, ADR-IAC-001 extension). Mirrors
// internal/httpapi/handlers_users.go's userDTO exactly — field names and
// shape, not a paraphrase. Kept hand-written, like the other admin clients,
// until a second consumer of the generated spec appears.
//
// WHY THIS IS A SEPARATE MODULE FROM `users.ts`: that file wraps
// `GET /v1/admin/tenant-users` — a DIFFERENT, PermAdmin-gated endpoint that
// returns only the ACTIVE members of the caller's OWN tenant, built for
// pickers (the incident assignee field, the on-call roster) that need a
// small, tenant-scoped directory. This module wraps `GET/POST/PATCH
// /v1/admin/users` — the PLATFORM-WIDE registry of every user regardless of
// tenant or membership, gated `requirePlatformAdmin` (a strictly higher bar,
// server.go:403-406) and able to create/rename/transition users, which
// `users.ts`'s endpoint cannot do at all. Overloading one module for both
// would blur a real authorization-boundary and shape difference; see
// `users.ts`'s own doc comment, which records the same split from its side.

/** The whole lifecycle (`internal/domain/user.go`). Deactivation is terminal. */
export const USER_STATUSES = ['invited', 'active', 'suspended', 'deactivated'] as const;
export type UserStatus = (typeof USER_STATUSES)[number];

export interface UserDTO {
  user_id: string;
  email: string;
  display_name: string;
  status: UserStatus;
  row_version: number;
  created_at: string;
  updated_at: string;
}

/**
 * A client-side mirror of `userTransitions` (`internal/domain/user.go:45`) —
 * kept in lock-step with the Go table deliberately, the same discipline
 * `incidents.ts`'s `INCIDENT_TRANSITIONS` follows for its own source of
 * truth. `deactivated` has no outgoing edges: it is terminal
 * (ADR-IDENTITY-001 §8.3, `user.go:44`). An entry here that drifted from the
 * server's own `CanTransitionTo` would either offer an illegal move (caught
 * late, as a 409) or hide a legal one — there is no runtime "legal next
 * states" field on `userDTO` to derive this from instead.
 */
export const USER_TRANSITIONS: Record<UserStatus, UserStatus[]> = {
  invited: ['active', 'deactivated'],
  active: ['suspended', 'deactivated'],
  suspended: ['active', 'deactivated'],
  deactivated: [],
};

/** The legal next statuses from `status` — never anything `USER_TRANSITIONS` doesn't list. */
export function legalUserTransitions(status: UserStatus): UserStatus[] {
  return USER_TRANSITIONS[status];
}

/**
 * The bound on a single registry fetch — the same "one page, no keyset
 * chasing" posture `MEMBERSHIP_LIST_CAP`/`INCIDENT_LIST_CAP` document, for
 * the same reason: `listUsers` (`handlers_users.go:65`) carries no
 * next-page cursor in its response body.
 */
export const USER_LIST_CAP = 200;

/** GET /v1/admin/users — one bounded page of the global registry. */
export function listUsers(limit = USER_LIST_CAP, signal?: AbortSignal): Promise<{ items: UserDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(limit));
  return getJSON<{ items: UserDTO[] }>(`/v1/admin/users?${p.toString()}`, signal);
}

export interface CreateUserInput {
  email: string;
  display_name: string;
}

/**
 * POST /v1/admin/users — `createUserRequest` (`handlers_users.go:42`),
 * exactly `{email, display_name}`. `user_id` is minted server-side and never
 * accepted from the caller (`domain.NewUser`'s own doc comment — the
 * cross-tenant key-confusion defect Trust Register entry 1 records). Always
 * creates the user `invited` (`handlers_users.go:109`); it holds no access
 * until a membership is granted. A duplicate email returns `409`.
 */
export function createUser(input: CreateUserInput, signal?: AbortSignal): Promise<UserDTO> {
  return postJSON<UserDTO>('/v1/admin/users', input, signal);
}

/**
 * PATCH /v1/admin/users/{id} — renames the display name. `rowVersion` MUST
 * be the value last read for this user; a stale one returns `409`
 * (`ErrVersionMismatch`, `handlers_users.go:226`), the same "refetch, never
 * blind-retry" contract `transitionIncident`/`patchAlertRule` follow.
 *
 * This and `setUserStatus` are kept as two separate functions, not one
 * generic patch, because the server itself refuses a request naming both
 * fields (`patchUserRequest`'s handler, `handlers_users.go:190-200`) — one
 * or the other, never both, since each consumes its own row_version through
 * a different repository method. Two narrow functions make sending both
 * impossible at the call site rather than merely rejected server-side.
 */
export function renameUser(userId: string, rowVersion: number, displayName: string, signal?: AbortSignal): Promise<UserDTO> {
  return patchJSON<UserDTO>(
    `/v1/admin/users/${encodeURIComponent(userId)}`,
    { row_version: rowVersion, display_name: displayName },
    signal,
  );
}

/**
 * PATCH /v1/admin/users/{id} — a lifecycle transition. `rowVersion` MUST be
 * the value last read for this user. A stale row_version and an illegal
 * transition both return `409` (`ErrVersionMismatch` /
 * `ErrInvalidTransition` — `handlers_users.go:222-228`), so callers must
 * treat "someone else changed it" and "this move is no longer legal"
 * identically: refetch, never blind-retry. Callers must only ever pass a
 * status `legalUserTransitions` lists for the user's current status — the UI
 * offers no others, and the server is the final authority regardless.
 */
export function setUserStatus(userId: string, rowVersion: number, status: UserStatus, signal?: AbortSignal): Promise<UserDTO> {
  return patchJSON<UserDTO>(`/v1/admin/users/${encodeURIComponent(userId)}`, { row_version: rowVersion, status }, signal);
}
