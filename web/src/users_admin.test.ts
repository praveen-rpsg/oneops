import { describe, expect, it } from 'vitest';
import { USER_STATUSES, USER_TRANSITIONS, legalUserTransitions } from './users_admin';

// Pins the restated lifecycle to internal/domain/user.go's `userTransitions`
// (user.go:45) exactly — value, not just count. If user.go's table changes,
// this test must be updated in the same change, which is the point of
// restating rather than generating (the same discipline rbac.test.ts applies
// to internal/auth/rbac.go).
describe('USER_TRANSITIONS', () => {
  it('defines exactly the four documented statuses', () => {
    expect(USER_STATUSES.slice().sort()).toEqual(['active', 'deactivated', 'invited', 'suspended'].sort());
  });

  it('matches user.go exactly for invited', () => {
    expect(USER_TRANSITIONS.invited).toEqual(['active', 'deactivated']);
  });

  it('matches user.go exactly for active', () => {
    expect(USER_TRANSITIONS.active).toEqual(['suspended', 'deactivated']);
  });

  it('matches user.go exactly for suspended', () => {
    expect(USER_TRANSITIONS.suspended).toEqual(['active', 'deactivated']);
  });

  it('matches user.go exactly for deactivated: terminal, no outgoing edges', () => {
    expect(USER_TRANSITIONS.deactivated).toEqual([]);
  });
});

describe('legalUserTransitions', () => {
  it('returns the same array USER_TRANSITIONS lists, never inventing an edge', () => {
    for (const status of USER_STATUSES) {
      expect(legalUserTransitions(status)).toEqual(USER_TRANSITIONS[status]);
    }
  });
});
