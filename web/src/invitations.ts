import { deleteJSON, getJSON, postJSON } from './api';

// Typed contract for the invitation admin endpoints (E-ID.4a, ADR-IAC-003)
// AND the unauthenticated redeem endpoint (E-ID.4b, ADR-IAC-004). Mirrors
// `internal/httpapi/handlers_invitations.go` field-for-field, confirmed
// against the Go before this module was written, not paraphrased.
//
// - `POST /v1/admin/invitations` (`createInvitation`, PermAdmin) — body is
//   `{email}` ONLY; org/tenant are resolved server-side from the caller's
//   context and are never accepted from the request (a cross-tenant
//   key-confusion class this endpoint deliberately closes off). Returns
//   `201` + the invitation DTO PLUS the one-time plaintext `token` — the
//   ONLY response in the contract that ever carries it. Lose it and there is
//   no recovery: `domain.Invitation` stores only its hash.
// - `GET /v1/admin/invitations` (`listInvitations`, PermAdmin) — one bounded,
//   keyset-paged list of the caller's own org's invitations. `invitationDTO`
//   has no token/token_hash field, ever.
// - `DELETE /v1/admin/invitations/{id}` (`revokeInvitation`, PermAdmin) —
//   withdraws a pending invitation; returns the revoked row (200), but this
//   module discards it like `revokeMembership` does — the caller refetches
//   the list (ADR-ACT-001 §3).
// - `POST /auth/invitations/redeem` (`redeemInvitation`, UNAUTHENTICATED,
//   top-level — NOT under `/v1`) — body is `{token}` ONLY; no bearer token is
//   sent or required (there is no session yet — that is the entire point of
//   this endpoint). Every failure cause (unknown/expired/revoked/redeemed/
//   suspended account) is the SAME generic `400`, by design — an
//   enumeration-resistant contract this client must not try to see through.

/** The whole lifecycle (`internal/domain/invitation.go`'s `Status`, mirrored in `openapi.yaml`'s `Invitation.status` enum). */
export const INVITATION_STATUSES = ['pending', 'redeemed', 'revoked', 'expired'] as const;
export type InvitationStatus = (typeof INVITATION_STATUSES)[number];

/**
 * `invitationDTO` (`handlers_invitations.go:35`), field-for-field.
 * Deliberately has no `token`/`token_hash` field — see the module doc
 * comment above.
 */
export interface InvitationDTO {
  invitation_id: string;
  org_id: string;
  email: string;
  status: InvitationStatus;
  expires_at: string;
  created_at: string;
  redeemed_at?: string;
}

/**
 * `createInvitationResponse` (`handlers_invitations.go:68`) — the invitation
 * plus the one-time plaintext `token`. This type must never be persisted or
 * logged by a caller; it exists only to be shown once and then discarded.
 */
export interface CreateInvitationResponse extends InvitationDTO {
  token: string;
}

/**
 * The bound on a single list fetch — the same "one page, no keyset chasing"
 * posture `MEMBERSHIP_LIST_CAP`/`USER_LIST_CAP` document, for the same
 * reason: this screen shows recent invitations, not an archive.
 */
export const INVITATION_LIST_CAP = 200;

export interface CreateInvitationInput {
  email: string;
}

/**
 * POST /v1/admin/invitations — mints an invitation into the CALLER'S OWN
 * organization and returns the one-time plaintext token. There is no
 * `org_id`/`tenant_id` field to send: the server resolves the target from
 * the authenticated context only (`createInvitationRequest` has no such
 * field to smuggle one through).
 */
export function createInvitation(input: CreateInvitationInput, signal?: AbortSignal): Promise<CreateInvitationResponse> {
  return postJSON<CreateInvitationResponse>('/v1/admin/invitations', input, signal);
}

/** GET /v1/admin/invitations — one bounded page of the caller's own org's invitations. */
export function listInvitations(limit = INVITATION_LIST_CAP, signal?: AbortSignal): Promise<{ items: InvitationDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(limit));
  return getJSON<{ items: InvitationDTO[] }>(`/v1/admin/invitations?${p.toString()}`, signal);
}

/**
 * DELETE /v1/admin/invitations/{id} — revokes a pending invitation. No
 * request body, matching `revokeMembership`'s confirmed shape; the caller
 * refetches the list rather than reading the returned row.
 */
export function revokeInvitation(invitationId: string, signal?: AbortSignal): Promise<void> {
  return deleteJSON(`/v1/admin/invitations/${encodeURIComponent(invitationId)}`, signal);
}

export interface RedeemInvitationResponse {
  organization: string;
}

/**
 * POST /auth/invitations/redeem — UNAUTHENTICATED (ADR-IAC-004). Deliberately
 * calls `postJSON` against the top-level path, not `/v1/...`: `postJSON`
 * itself only attaches an `Authorization` header when `getToken()` returns
 * one, so an invitee with no session sends none — exactly this endpoint's
 * contract (`security: []` in `openapi.yaml`; `s.authenticate` is not on this
 * route). Every failure cause collapses to the identical generic `400`; this
 * client does not, and must not, try to distinguish them.
 */
export function redeemInvitation(token: string, signal?: AbortSignal): Promise<RedeemInvitationResponse> {
  return postJSON<RedeemInvitationResponse>('/auth/invitations/redeem', { token }, signal);
}
