import { deleteJSON, getJSON, postJSON } from './api';

// Typed contract for GET/POST/DELETE /v1/admin/memberships{,/{id}}.
// Mirrors internal/httpapi/handlers_memberships.go's membershipDTO exactly —
// field names and shape, not a paraphrase. Confirmed against the Go before
// this module was written (E-ID.2):
//
// - GET /v1/admin/memberships?org_id=&limit=&after= (listMemberships,
//   handlers_memberships.go:52) — REQUIRES org_id (422 "org_id is required"
//   without it). Row-level security already confines the result to the
//   caller's own tenant; org_id narrows further to one organisation inside
//   it. Returns `{items:[...]}` with no next-page cursor in the body (the
//   same "one bounded page" shape listAlertRules/listMaintenanceWindows use).
//   `MembershipStore.ListByOrg` (internal/store/postgres/membership_store.go)
//   carries NO status predicate — active AND revoked rows both come back, so
//   this screen must render status per row rather than assume every row is
//   current.
// - POST /v1/admin/memberships (grantMembershipRequest,
//   handlers_memberships.go:41-44) — exactly `{org_id, user_id}`.
//   membership_id is minted server-side and never accepted from the caller.
//   Re-granting an already-active membership is a no-op that returns the
//   existing row (MembershipStore.Grant's own doc comment); granting a
//   previously revoked membership reactivates it. Returns 201 + the full
//   membershipDTO.
// - DELETE /v1/admin/memberships/{id} (revokeMembership,
//   handlers_memberships.go:129) — CONFIRMED CONTRACT FACT: takes no request
//   body and no row_version (domain.MembershipRepository.Revoke(ctx,
//   membershipID) has no optimistic-lock parameter), the same asymmetry
//   ADR-HARD-003 already decided to accept for the other four config
//   deletes. Unlike those, this one returns `200` + the revoked
//   membershipDTO rather than `204` — this module does not need that body
//   (the console refetches the list after any mutation, ADR-ACT-001 §3), so
//   `deleteJSON` — which never parses a success body — is still the right
//   helper. Revoking an already-revoked membership is rejected `409`
//   (ErrConflict): the caller should not offer the action once the row is
//   already revoked, but a race is still possible and surfaces like any
//   other action error.
//
// ORG ID SOURCING GAP (recorded, not invented around): neither of these
// endpoints' org_id can be discovered from anywhere else the console reaches
// today. It is not a JWT claim (web/src/auth.ts decodes sub/roles only), and
// the one endpoint that could resolve it — organisation lookup by tenant
// (domain.OrganizationRepository.GetByTenant) — has no HTTP route at all;
// every /admin/organizations/* route is requirePlatformAdmin
// (internal/httpapi/server.go:415-418), a strictly more privileged,
// cross-tenant gate than the PermAdmin tier these membership endpoints use
// (ADR-ACT-001 §2 already named exactly this shape of gap for a different
// endpoint). See routes/MembersPage.tsx's own doc comment for the resulting
// UX choice (a persisted, manually-entered Organization ID) and
// ADR-IAC-002 for the decision record.

export interface MembershipDTO {
  membership_id: string;
  org_id: string;
  user_id: string;
  status: 'active' | 'revoked';
  row_version: number;
  created_at: string;
  updated_at: string;
}

/**
 * The bound on a single board fetch — the same "one page, no keyset chasing"
 * posture ALERT_RULE_LIST_CAP/MAINTENANCE_WINDOW_LIST_CAP document, for the
 * same reason: this list endpoint carries no next-page cursor in its
 * response body. Below `clampMembershipPage`'s server-side max (500).
 */
export const MEMBERSHIP_LIST_CAP = 200;

export function listMemberships(
  orgId: string,
  limit = MEMBERSHIP_LIST_CAP,
  signal?: AbortSignal,
): Promise<{ items: MembershipDTO[] }> {
  const p = new URLSearchParams();
  p.set('org_id', orgId);
  p.set('limit', String(limit));
  return getJSON<{ items: MembershipDTO[] }>(`/v1/admin/memberships?${p.toString()}`, signal);
}

export interface GrantMembershipInput {
  org_id: string;
  user_id: string;
}

/** POST /v1/admin/memberships — grantMembershipRequest, field-for-field. */
export function grantMembership(input: GrantMembershipInput, signal?: AbortSignal): Promise<MembershipDTO> {
  return postJSON<MembershipDTO>('/v1/admin/memberships', input, signal);
}

/**
 * DELETE /v1/admin/memberships/{id} — revokes a membership.
 *
 * CONFIRMED CONTRACT FACT (this story), not assumed: no `row_version`, no
 * request body (ADR-HARD-003's accepted asymmetry, same shape as
 * `deleteMaintenanceWindow`/`deleteAlertRule`). The confirm modal, not an
 * optimistic-lock check, is the guard on this destructive action.
 */
export function revokeMembership(membershipId: string, signal?: AbortSignal): Promise<void> {
  return deleteJSON(`/v1/admin/memberships/${encodeURIComponent(membershipId)}`, signal);
}
