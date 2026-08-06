import { describe, expect, it } from 'vitest';
import { PERMISSIONS, ROLE_PERMISSIONS, effectivePermissions } from './rbac';

// Asserts this file matches internal/auth/rbac.go's rolePermissions map
// exactly (value, not just count) — the contract E-ID.1 depends on. If
// rbac.go changes, this test must be updated in the same change, which is
// the point of restating rather than generating.
describe('ROLE_PERMISSIONS', () => {
  it('matches rbac.go exactly for oneops-reader', () => {
    expect(ROLE_PERMISSIONS['oneops-reader'].map((p) => p.value)).toEqual(['artifacts:read']);
  });

  it('matches rbac.go exactly for oneops-editor', () => {
    expect(ROLE_PERMISSIONS['oneops-editor'].map((p) => p.value)).toEqual(['artifacts:read', 'artifacts:write']);
  });

  it('matches rbac.go exactly for oneops-admin', () => {
    expect(ROLE_PERMISSIONS['oneops-admin'].map((p) => p.value)).toEqual([
      'artifacts:read',
      'artifacts:write',
      'artifacts:delete',
      'artifacts:admin',
    ]);
  });

  it('matches rbac.go exactly for oneops-platform-admin', () => {
    expect(ROLE_PERMISSIONS['oneops-platform-admin'].map((p) => p.value)).toEqual([
      'artifacts:read',
      'artifacts:write',
      'artifacts:delete',
      'artifacts:admin',
      'platform:admin',
    ]);
  });

  it('scopes platform:admin as platform and every other permission as tenant', () => {
    expect(PERMISSIONS.platformAdmin.scope).toBe('platform');
    expect(PERMISSIONS.read.scope).toBe('tenant');
    expect(PERMISSIONS.write.scope).toBe('tenant');
    expect(PERMISSIONS.delete.scope).toBe('tenant');
    expect(PERMISSIONS.admin.scope).toBe('tenant');
  });

  it('defines exactly the four documented roles', () => {
    expect(Object.keys(ROLE_PERMISSIONS).sort()).toEqual(
      ['oneops-admin', 'oneops-editor', 'oneops-platform-admin', 'oneops-reader'].sort(),
    );
  });
});

describe('effectivePermissions', () => {
  it('is the exact grant for a single role', () => {
    expect(effectivePermissions(['oneops-editor']).map((p) => p.value)).toEqual(['artifacts:read', 'artifacts:write']);
  });

  it('unions and dedupes permissions across multiple roles', () => {
    expect(effectivePermissions(['oneops-reader', 'oneops-editor']).map((p) => p.value)).toEqual([
      'artifacts:read',
      'artifacts:write',
    ]);
  });

  it('grants nothing for an empty or unrecognised role list', () => {
    expect(effectivePermissions([])).toEqual([]);
    expect(effectivePermissions(['not-a-real-role'])).toEqual([]);
  });
});
