import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { MembershipDTO, OrganizationDTO } from '../memberships';
import type { TenantUserDTO } from '../users';

// Exercises E-ID.2/E-ID.2a's Members screen: automatic org_id resolution via
// GET /admin/tenant-org (ADR-IAC-002, amended), list (joined with
// tenant-users for names, degrading to the raw id), grant, revoke (confirm
// -then-delete-then-refetch, ADR-HARD-003's no-row_version delete), the
// 403-graceful permission-needed state, and the manual-entry fallback for a
// non-403 tenant-org failure.

const org = (over: Partial<OrganizationDTO> = {}): OrganizationDTO => ({
  org_id: 'ORG-1',
  tenant_id: 'TEN-1',
  slug: 'acme',
  name: 'Acme Corp',
  status: 'active',
  row_version: 1,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  ...over,
});

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
  tenantOrg?: OrganizationDTO;
  /** GET /admin/tenant-org answers 403 — "not an admin", the permission state. */
  tenantOrgForbidden?: boolean;
  /** GET /admin/tenant-org answers 500 — falls back to manual entry. */
  tenantOrgError?: boolean;
  /** 'ERROR' makes POST /v1/admin/memberships fail 422. */
  grant?: 'ERROR';
  /** 'ERROR' makes DELETE /v1/admin/memberships/{id} fail 409. */
  revoke?: 'ERROR';
}

function routedFetch(fx: Fixture = {}) {
  const items = [...(fx.memberships ?? [membership(), unresolved, revoked])];
  const users = [...(fx.tenantUsers ?? [tenantUser()])];
  const theOrg = fx.tenantOrg ?? org();

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

    if (u.includes('/admin/tenant-org')) {
      if (fx.tenantOrgForbidden) return fail(403, { title: 'forbidden', detail: 'missing permission: artifacts:admin' });
      if (fx.tenantOrgError) return fail(500, { title: 'internal error', detail: 'boom' });
      return ok(theOrg);
    }

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
      return ok({ items: users });
    }

    if (u.includes('/admin/memberships')) {
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

describe('organization auto-resolution', () => {
  it('resolves org_id from GET /admin/tenant-org and loads members with no manual entry', async () => {
    vi.stubGlobal('fetch', routedFetch());
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Members/ })).toBeInTheDocument();
    expect(await screen.findByText('Acme Corp')).toBeInTheDocument();
    expect(screen.getByText('ORG-1')).toBeInTheDocument();
    expect(await screen.findByText('Alice (alice@example.com)')).toBeInTheDocument();

    // The happy path never shows the manual Organization ID input.
    expect(screen.queryByLabelText('Organization ID')).not.toBeInTheDocument();
  });

  it('renders the clean permission-needed state on a 403 from tenant-org, not the manual fallback', async () => {
    vi.stubGlobal('fetch', routedFetch({ tenantOrgForbidden: true }));
    renderApp();

    expect(
      await screen.findByText('You need tenant-admin permission to manage members.'),
    ).toBeInTheDocument();
    expect(screen.queryByLabelText('Organization ID')).not.toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('falls back to manual Organization ID entry on a non-403 tenant-org failure', async () => {
    vi.stubGlobal('fetch', routedFetch({ tenantOrgError: true }));
    renderApp();

    const input = await screen.findByLabelText('Organization ID');
    expect(screen.getByText(/Automatic organization lookup failed/)).toBeInTheDocument();
    expect(screen.queryByText('You need tenant-admin permission to manage members.')).not.toBeInTheDocument();

    fireEvent.change(input, { target: { value: 'ORG-1' } });
    fireEvent.click(screen.getByRole('button', { name: 'Load members' }));

    expect(await screen.findByText('Alice (alice@example.com)')).toBeInTheDocument();
  });
});

describe('members list', () => {
  it('joins names from tenant-users and degrades to the raw id when unresolved, showing status per row', async () => {
    vi.stubGlobal('fetch', routedFetch());
    renderApp();

    expect(await screen.findByText('Alice (alice@example.com)')).toBeInTheDocument();
    // USR-2 has no tenant-users entry — the row degrades to the bare id.
    expect(screen.getByText('USR-2')).toBeInTheDocument();

    const statuses = screen.getAllByText('Active');
    expect(statuses.length).toBe(2);
    expect(screen.getByText('Revoked')).toBeInTheDocument();

    // Revoke is only offered on active rows.
    expect(screen.getAllByRole('button', { name: 'Revoke' }).length).toBe(2);
  });
});

describe('grant membership', () => {
  it('posts the exact {org_id, user_id} body and refetches the list', async () => {
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
