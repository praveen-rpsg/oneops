import { getJSON } from './api';

// Typed contract for GET /v1/admin/users, mirroring internal/httpapi/
// handlers_users.go's userDTO exactly. Kept hand-written and minimal — the
// console's only current consumer is the incident assignee picker
// (components/IncidentDetail.tsx, ADR-ACT-001).
//
// CONFIRMED CONTRACT GAP (ADR-ACT-001): this route is `requirePlatformAdmin`
// (internal/httpapi/server.go), a strictly more privileged, cross-tenant
// gate than the `PermAdmin` tenant-administration permission the incident
// endpoints use. A tenant-scoped operator who can manage incidents is not
// guaranteed to be a platform administrator, so this call can 403 for a real
// deployment's normal incident operator even though it succeeds for the
// system-tenant identity `AuthEnabled=false` local dev synthesizes
// (internal/httpapi/middleware.go's `authenticate`). The assignee picker
// below is written to degrade to a free-text user id input on ANY failure
// here (403 included) rather than assume this endpoint is always reachable.

export type UserStatus = 'invited' | 'active' | 'suspended' | 'deactivated';

export interface UserDTO {
  user_id: string;
  email: string;
  display_name: string;
  status: UserStatus;
  row_version: number;
  created_at: string;
  updated_at: string;
}

/** A page of the platform's users, capped well above what one tenant's picker needs. */
export function listUsers(limit = 200, signal?: AbortSignal): Promise<{ items: UserDTO[] }> {
  const p = new URLSearchParams();
  p.set('limit', String(limit));
  return getJSON<{ items: UserDTO[] }>(`/v1/admin/users?${p.toString()}`, signal);
}
