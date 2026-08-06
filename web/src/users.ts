import { getJSON } from './api';

// Typed contract for GET /v1/admin/tenant-users, mirroring
// internal/httpapi/handlers_tenant_users.go's tenantUserDTO exactly
// (ADR-ACT-003). This is the tenant-scoped sibling of the platform-wide
// GET /v1/admin/users this module used before E-ACT.4: PermAdmin-gated (the
// same tier the incident/on-call administration endpoints already use), not
// requirePlatformAdmin, so a real tenant operator — not just the local-dev
// AuthEnabled=false identity — can reach it. Returns ACTIVE members of the
// CALLER'S OWN tenant only, isolated by row-level security (ADR-ACT-003 §2),
// each of whom also holds an active platform account (ADR-ACT-003 §4).
//
// This closes the ADR-ACT-001 §2 gap for the incident assignee picker
// (components/IncidentDetail.tsx) and is the on-call roster picker's only
// user source (components/OnCallScheduleDetail.tsx) — both rewired/wired in
// E-ACT.4. The picker still degrades to a free-text input on ANY failure
// here (a brand-new or misconfigured tenant, a network error, or simply a
// caller who does not hold PermAdmin) rather than assuming this endpoint is
// always reachable — the same defensive posture ADR-ACT-001 established for
// the platform-wide endpoint it replaces here.

export interface TenantUserDTO {
  user_id: string;
  email: string;
  display_name: string;
}

/** A page of the caller's own tenant's active users, capped well above what one picker needs. */
export function listTenantUsers(limit = 200, signal?: AbortSignal): Promise<{ items: TenantUserDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(limit));
  return getJSON<{ items: TenantUserDTO[] }>(`/v1/admin/tenant-users?${p.toString()}`, signal);
}
