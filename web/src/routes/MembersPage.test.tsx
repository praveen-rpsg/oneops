import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { MembershipDTO } from '../memberships';
import type { TenantUserDTO } from '../users';

// Exercises E-ID.2/ADR-IAC-002's Members screen: list (joined with
// tenant-users for names, degrading to the raw id), grant, revoke (confirm
// -then-delete-then-refetch, ADR-HARD-003's no-row_version delete), and the
// 403-graceful permission-needed state.

const ORG_ID_KEY = 'oneops.membership.org_id';

const membership = (over: Partial<MembershipDTO> = {}): MembershipDTO => ({
  membership_id: 'MSHIP-1',
  org_id: 'ORG-1',
  user_id: 'USR-1',
  status: 'active',
  row_version: 1,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  ...over,
});

const unresolved = membership({ membership_id: 'MSHIP-2', user_id: 'USR-2' });
const revoked = membership({ membership_id: 'MSHIP-3', user_id: 'USR-3', status: 'revoked' });

const tenantUser = (over: Partial<TenantUserDTO> = {}): TenantUserDTO => ({
  user_id: 'USR-1',
  email: 'alice@example.com',
  display_name: 'Alice',
  ...over,
});

interface Fixture {
  memberships?: MembershipDTO[];
  tenantUsers?: TenantUserDTO[];
  /** Both GET endpoints answer 403, mimicking a caller without PermAdmin. */
  forbidden?: boolean;
  /** 'ERROR' makes POST /v1/admin/memberships fail 422. */
  grant?: 'ERROR';
  /** 'ERROR' makes DELETE /v1/admin/memberships/{id} fail 409. */
  revoke?: 'ERROR';
}

function routedFetch(fx: Fixture = {}) {
  const items = [...(fx.memberships ?? [membership(), unresolved, revoked])];
  const users = [...(fx.tenantUsers ?? [tenantUser()])];

  return vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url);
    const method = init?.method ?? 'GET';
    const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
    const fail = (status: number, body: Record<string, unknown>) => ({
      ok: false,
      status,
      json: async () => ({ status, ...body }),
    });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    if (method === 'POST' && /\/admin\/memberships$/.test(u)) {
      if (fx.grant === 'ERROR') {
        return fail(422, { title: 'validation failed', detail: 'user_id: no such user' });
      }
      const body = JSON.parse(String(init?.body)) as { org_id: string; user_id: string };
      const created = membership({ membership_id: 'MSHIP-NEW', org_id: body.org_id, user_id: body.user_id });
      items.push(created);
      return ok(created, 201);
    }

    const idMatch = u.match(/\/admin\/memberships\/([^/?]+)/);
    if (method === 'DELETE' && idMatch) {
      if (fx.revoke === 'ERROR') {
        return fail(409, { title: 'conflict', detail: 'already revoked' });
      }
      const i = items.findIndex((m) => m.membership_id === idMatch[1]);
      if (i >= 0) items[i] = { ...items[i], status: 'revoked' };
      return ok(items[i], 200);
    }

    if (u.includes('/admin/tenant-users')) {
      if (fx.forbidden) return fail(403, { title: 'forbidden', detail: 'missing permission: artifacts:admin' });
      return ok({ items: users });
    }

    if (u.includes('/admin/memberships')) {
      if (fx.forbidden) return fail(403, { title: 'forbidden', detail: 'missing permission: artifacts:admin' });
      return ok({ items });
    }

    return ok({});
  });
}

beforeEach(() => {
  window.history.replaceState(null, '', '/members');
  window.sessionStorage.clear();
});
afterEach(() => {
  vi.unstubAllGlobals();
  window.sessionStorage.clear();
});

describe('members list', () => {
  it('joins names from tenant-users and degrades to the raw id when unresolved, showing status per row', async () => {
    window.sessionStorage.setItem(ORG_ID_KEY, 'ORG-1');
    vi.stubGlobal('fetch', routedFetch());
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Members/ })).toBeInTheDocument();
    expect(await screen.findByText('Alice (alice@example.com)')).toBeInTheDocument();
    // USR-2 has no tenant-users entry — the row degrades to the bare id.
    expect(screen.getByText('USR-2')).toBeInTheDocument();

    const statuses = screen.getAllByText('Active');
    expect(statuses.length).toBe(2);
    expect(screen.getByText('Revoked')).toBeInTheDocument();

    // Revoke is only offered on active rows.
    expect(screen.getAllByRole('button', { name: 'Revoke' }).length).toBe(2);
  });

  it('prompts for an Organization ID when none is remembered, and loads once one is entered', async () => {
    vi.stubGlobal('fetch', routedFetch());
    renderApp();

    expect(await screen.findByText('Enter an Organization ID above to load its members.')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Organization ID'), { target: { value: 'ORG-1' } });
    fireEvent.click(screen.getByRole('button', { name: 'Load members' }));

    expect(await screen.findByText('Alice (alice@example.com)')).toBeInTheDocument();
  });
});

describe('grant membership', () => {
  it('posts the exact {org_id, user_id} body and refetches the list', async () => {
    window.sessionStorage.setItem(ORG_ID_KEY, 'ORG-1');
    const f = routedFetch();
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByText('Alice (alice@example.com)');
    const listCallsBefore = f.mock.calls.filter(([u]) => String(u).includes('/admin/memberships?')).length;

    fireEvent.click(screen.getByRole('button', { name: 'Grant membership' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('User ID'), { target: { value: 'USR-9' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Grant' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const postCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/memberships') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    const body = JSON.parse(String((postCall[1] as RequestInit).body)) as Record<string, unknown>;
    expect(body).toEqual({ org_id: 'ORG-1', user_id: 'USR-9' });

    await waitFor(() =>
      expect(f.mock.calls.filter(([u]) => String(u).includes('/admin/memberships?')).length).toBeGreaterThan(
        listCallsBefore,
      ),
    );
  });

  it('surfaces a grant validation error and keeps the dialog open', async () => {
    window.sessionStorage.setItem(ORG_ID_KEY, 'ORG-1');
    vi.stubGlobal('fetch', routedFetch({ grant: 'ERROR' }));
    renderApp();

    await screen.findByText('Alice (alice@example.com)');
    fireEvent.click(screen.getByRole('button', { name: 'Grant membership' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('User ID'), { target: { value: 'USR-9' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Grant' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('no such user');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});

describe('revoke membership', () => {
  it('confirms, then deletes with no row_version, then refetches the list', async () => {
    window.sessionStorage.setItem(ORG_ID_KEY, 'ORG-1');
    const f = routedFetch();
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByText('Alice (alice@example.com)');
    const listCallsBefore = f.mock.calls.filter(([u]) => String(u).includes('/admin/memberships?')).length;

    const row = screen.getByRole('row', { name: /Alice/ });
    fireEvent.click(within(row).getByRole('button', { name: 'Revoke' }));

    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('Revoke membership for Alice (alice@example.com)?');
    expect(f.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Revoke membership' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const deleteCall = f.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')!;
    expect(String(deleteCall[0])).toContain('/admin/memberships/MSHIP-1');
    expect(deleteCall[1]).not.toHaveProperty('body');

    await waitFor(() =>
      expect(f.mock.calls.filter(([u]) => String(u).includes('/admin/memberships?')).length).toBeGreaterThan(
        listCallsBefore,
      ),
    );
  });

  it('surfaces a revoke error inline rather than failing silently', async () => {
    window.sessionStorage.setItem(ORG_ID_KEY, 'ORG-1');
    vi.stubGlobal('fetch', routedFetch({ revoke: 'ERROR' }));
    renderApp();

    await screen.findByText('Alice (alice@example.com)');
    const row = screen.getByRole('row', { name: /Alice/ });
    fireEvent.click(within(row).getByRole('button', { name: 'Revoke' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Revoke membership' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('already revoked');
    // Still shown as active — no silent removal/status flip on a failed revoke.
    const statuses = await screen.findAllByText('Active');
    expect(statuses.length).toBe(2);
  });
});

describe('403 — missing tenant-admin permission', () => {
  it('renders a clean permission-needed state instead of crashing or dumping the raw error', async () => {
    window.sessionStorage.setItem(ORG_ID_KEY, 'ORG-1');
    vi.stubGlobal('fetch', routedFetch({ forbidden: true }));
    renderApp();

    expect(
      await screen.findByText('You need tenant-admin permission to manage members.'),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText('Organization ID')).not.toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });
});
