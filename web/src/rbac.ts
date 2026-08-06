// Restates the OneOps role→permission map owned by `internal/auth/rbac.go`.
// Source of truth is the Go file — this is restated, not generated — keep in
// sync. It exists so the Administration console (E-ID.1) can show a caller
// their own effective permissions and a read-only reference table, without
// the browser making any authorization decision itself: the server enforces
// every one of these grants independently (internal/auth.HasPermission), and
// nothing here is consulted on that path.

export const ROLES = ['oneops-reader', 'oneops-editor', 'oneops-admin', 'oneops-platform-admin'] as const;
export type Role = (typeof ROLES)[number];

export type PermissionScope = 'tenant' | 'platform';

export interface PermissionInfo {
  value: string;
  label: string;
  scope: PermissionScope;
}

// Every tenant-scoped permission's effect is confined by row-level security
// to the caller's own tenant; PermPlatformAdmin is the one platform-scoped
// permission and is granted/checked separately (rbac.go's own reasoning).
export const PERMISSIONS = {
  read: { value: 'artifacts:read', label: 'Read', scope: 'tenant' },
  write: { value: 'artifacts:write', label: 'Write', scope: 'tenant' },
  delete: { value: 'artifacts:delete', label: 'Delete', scope: 'tenant' },
  admin: { value: 'artifacts:admin', label: 'Tenant admin', scope: 'tenant' },
  platformAdmin: { value: 'platform:admin', label: 'Platform admin', scope: 'platform' },
} as const satisfies Record<string, PermissionInfo>;

export type Permission = (typeof PERMISSIONS)[keyof typeof PERMISSIONS];

/**
 * rolePermissions in rbac.go, restated field-for-field. The match is EXACT —
 * a role grants exactly the permissions listed here, never more (rbac.go's
 * `HasPermission` doc comment records why a wildcard was a real defect).
 */
export const ROLE_PERMISSIONS: Record<Role, Permission[]> = {
  'oneops-reader': [PERMISSIONS.read],
  'oneops-editor': [PERMISSIONS.read, PERMISSIONS.write],
  'oneops-admin': [PERMISSIONS.read, PERMISSIONS.write, PERMISSIONS.delete, PERMISSIONS.admin],
  'oneops-platform-admin': [
    PERMISSIONS.read,
    PERMISSIONS.write,
    PERMISSIONS.delete,
    PERMISSIONS.admin,
    PERMISSIONS.platformAdmin,
  ],
};

function isRole(r: string): r is Role {
  return (ROLES as readonly string[]).includes(r);
}

/**
 * The union of every permission granted by `roles`, deduped by permission
 * value, in the stable order PERMISSIONS lists them — not authorization
 * (display only, see auth.ts' getRoles). Unknown role names grant nothing,
 * matching rbac.go's map lookup, which returns no entries for a role it
 * doesn't recognise.
 */
export function effectivePermissions(roles: string[]): Permission[] {
  const seen = new Set<string>();
  const result: Permission[] = [];
  for (const perm of Object.values(PERMISSIONS)) {
    const granted = roles.some((r) => isRole(r) && ROLE_PERMISSIONS[r].includes(perm));
    if (granted && !seen.has(perm.value)) {
      seen.add(perm.value);
      result.push(perm);
    }
  }
  return result;
}
