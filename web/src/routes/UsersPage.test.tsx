import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { UserDTO } from '../users_admin';

// Exercises E-ID.3's Users screen: list, create (POST /v1/admin/users),
// rename and status-change (both PATCH /v1/admin/users/{id}, ADR-ACT-001's
// refetch-after/409-refetches-with-notice discipline), legal-transitions-only
// gating (internal/domain/user.go's userTransitions), the terminal-deactivate
// confirm, and the 403-graceful permission state (requirePlatformAdmin,
// server.go:403-406 — a higher bar than MembersPage's PermAdmin).

const user = (over: Partial<UserDTO> = {}): UserDTO => ({
  user_id: 'USR-1',
  email: 'alice@example.com',
  display_name: 'Alice',
  status: 'invited',
  row_version: 1,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  ...over,
});

const activeUser = user({ user_id: 'USR-2', email: 'bob@example.com', display_name: 'Bob', status: 'active' });
const suspendedUser = user({ user_id: 'USR-3', email: 'carol@example.com', display_name: 'Carol', status: 'suspended' });
const deactivatedUser = user({ user_id: 'USR-4', email: 'dave@example.com', display_name: 'Dave', status: 'deactivated' });

interface Fixture {
  items?: UserDTO[];
  /** GET /admin/users answers 403 — not a platform admin. */
  forbidden?: boolean;
  /** POST /v1/admin/users fails 422. */
  createError?: boolean;
  /** PATCH .../{id} (display_name) fails 409. */
  renameConflict?: boolean;
  /** PATCH .../{id} (status) fails 409. */
  statusConflict?: boolean;
}

function routedFetch(fx: Fixture = {}) {
  const items = [...(fx.items ?? [user(), activeUser, suspendedUser, deactivatedUser])];

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

    if (method === 'POST' && /\/admin\/users$/.test(u)) {
      if (fx.createError) return fail(422, { title: 'validation failed', detail: 'email: must be a valid email address' });
      const body = JSON.parse(String(init?.body)) as { email: string; display_name: string };
      const created = user({
        user_id: 'USR-NEW',
        email: body.email,
        display_name: body.display_name,
        status: 'invited',
        row_version: 1,
      });
      items.push(created);
      return ok(created, 201);
    }

    const idMatch = u.match(/\/admin\/users\/([^/?]+)$/);
    if (method === 'PATCH' && idMatch) {
      const body = JSON.parse(String(init?.body)) as { row_version: number; display_name?: string; status?: string };
      const i = items.findIndex((it) => it.user_id === idMatch[1]);
      if (body.display_name !== undefined && fx.renameConflict) {
        return fail(409, { title: 'conflict', detail: 'user was modified concurrently; re-read and retry' });
      }
      if (body.status !== undefined && fx.statusConflict) {
        return fail(409, { title: 'conflict', detail: 'user was modified concurrently; re-read and retry' });
      }
      if (i >= 0) {
        items[i] = {
          ...items[i],
          display_name: body.display_name ?? items[i].display_name,
          status: (body.status as UserDTO['status']) ?? items[i].status,
          row_version: items[i].row_version + 1,
        };
      }
      return ok(items[i], 200);
    }

    if (u.includes('/admin/users')) {
      if (fx.forbidden) return fail(403, { title: 'forbidden', detail: 'missing permission: platform:admin' });
      return ok({ items });
    }

    return ok({});
  });
}

beforeEach(() => {
  window.history.replaceState(null, '', '/users');
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe('users list', () => {
  it('renders email, display name, status and timestamps', async () => {
    vi.stubGlobal('fetch', routedFetch());
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Users/ })).toBeInTheDocument();
    expect(await screen.findByText('alice@example.com')).toBeInTheDocument();
    expect(screen.getByText('Alice')).toBeInTheDocument();
    expect(screen.getByText('Invited')).toBeInTheDocument();
    expect(screen.getByText('Active')).toBeInTheDocument();
    expect(screen.getByText('Suspended')).toBeInTheDocument();
    expect(screen.getByText('Deactivated')).toBeInTheDocument();
  });

  it('offers only legal next statuses per row, and none for a deactivated (terminal) user', async () => {
    vi.stubGlobal('fetch', routedFetch());
    renderApp();

    await screen.findByText('alice@example.com');

    const invitedRow = screen.getByRole('row', { name: /alice@example\.com/ });
    expect(within(invitedRow).getByRole('button', { name: 'Activate' })).toBeInTheDocument();
    expect(within(invitedRow).getByRole('button', { name: 'Deactivate' })).toBeInTheDocument();
    expect(within(invitedRow).queryByRole('button', { name: 'Suspend' })).not.toBeInTheDocument();

    const activeRow = screen.getByRole('row', { name: /bob@example\.com/ });
    expect(within(activeRow).getByRole('button', { name: 'Suspend' })).toBeInTheDocument();
    expect(within(activeRow).getByRole('button', { name: 'Deactivate' })).toBeInTheDocument();
    expect(within(activeRow).queryByRole('button', { name: 'Activate' })).not.toBeInTheDocument();

    const suspendedRow = screen.getByRole('row', { name: /carol@example\.com/ });
    expect(within(suspendedRow).getByRole('button', { name: 'Activate' })).toBeInTheDocument();
    expect(within(suspendedRow).getByRole('button', { name: 'Deactivate' })).toBeInTheDocument();

    const deactivatedRow = screen.getByRole('row', { name: /dave@example\.com/ });
    expect(within(deactivatedRow).queryByRole('button', { name: 'Activate' })).not.toBeInTheDocument();
    expect(within(deactivatedRow).queryByRole('button', { name: 'Suspend' })).not.toBeInTheDocument();
    expect(within(deactivatedRow).queryByRole('button', { name: 'Deactivate' })).not.toBeInTheDocument();
    // Rename remains available regardless of lifecycle status.
    expect(within(deactivatedRow).getByRole('button', { name: 'Rename' })).toBeInTheDocument();
  });
});

describe('create user', () => {
  it('posts the exact {email, display_name} body and refetches the list', async () => {
    const f = routedFetch();
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByText('alice@example.com');
    const listCallsBefore = f.mock.calls.filter(
      ([u, init]) => String(u).includes('/admin/users?') && (init as RequestInit | undefined)?.method === undefined,
    ).length;

    fireEvent.click(screen.getByRole('button', { name: 'Create user' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Email'), { target: { value: 'new@example.com' } });
    fireEvent.change(within(dialog).getByLabelText('Display name'), { target: { value: 'New Person' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const postCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/users') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    const body = JSON.parse(String((postCall[1] as RequestInit).body)) as Record<string, unknown>;
    expect(body).toEqual({ email: 'new@example.com', display_name: 'New Person' });

    await waitFor(() =>
      expect(
        f.mock.calls.filter(([u, init]) => String(u).includes('/admin/users?') && (init as RequestInit | undefined)?.method === undefined)
          .length,
      ).toBeGreaterThan(listCallsBefore),
    );
    expect(await screen.findByText('new@example.com')).toBeInTheDocument();
  });

  it('rejects an invalid email client-side without ever calling the server', async () => {
    const f = routedFetch();
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByText('alice@example.com');
    fireEvent.click(screen.getByRole('button', { name: 'Create user' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Email'), { target: { value: 'not-an-email' } });

    expect(within(dialog).getByText('Must be a valid email address.')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
  });

  it('surfaces a server validation error and keeps the dialog open', async () => {
    vi.stubGlobal('fetch', routedFetch({ createError: true }));
    renderApp();

    await screen.findByText('alice@example.com');
    fireEvent.click(screen.getByRole('button', { name: 'Create user' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Email'), { target: { value: 'new@example.com' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('must be a valid email address');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});

describe('rename user', () => {
  it('patches display_name + row_version and refetches', async () => {
    const f = routedFetch();
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByText('alice@example.com');
    const row = screen.getByRole('row', { name: /alice@example\.com/ });
    fireEvent.click(within(row).getByRole('button', { name: 'Rename' }));

    const dialog = await screen.findByRole('dialog');
    const input = within(dialog).getByLabelText('Display name') as HTMLInputElement;
    expect(input.value).toBe('Alice');
    fireEvent.change(input, { target: { value: 'Alice Renamed' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Rename' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const patchCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/users/USR-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    const body = JSON.parse(String((patchCall[1] as RequestInit).body)) as Record<string, unknown>;
    expect(body).toEqual({ row_version: 1, display_name: 'Alice Renamed' });

    expect(await screen.findByText('Alice Renamed')).toBeInTheDocument();
  });

  it('on 409, closes the dialog, shows the conflict notice, and does not blindly retry', async () => {
    const f = routedFetch({ renameConflict: true });
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByText('alice@example.com');
    const row = screen.getByRole('row', { name: /alice@example\.com/ });
    fireEvent.click(within(row).getByRole('button', { name: 'Rename' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Display name'), { target: { value: 'Alice Renamed' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Rename' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(await screen.findByText('Changed since you loaded it')).toBeInTheDocument();

    const patchCalls = f.mock.calls.filter(
      ([u, init]) => String(u).endsWith('/admin/users/USR-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    );
    expect(patchCalls.length).toBe(1);
    // Name is unchanged — the conflicting write was never applied, and the screen did not retry it.
    expect(screen.getByText('Alice')).toBeInTheDocument();
  });
});

describe('status change', () => {
  it('activate fires directly (no confirm) and patches status + row_version', async () => {
    const f = routedFetch();
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByText('alice@example.com');
    const row = screen.getByRole('row', { name: /alice@example\.com/ });
    fireEvent.click(within(row).getByRole('button', { name: 'Activate' }));

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();

    await waitFor(() => {
      const patchCall = f.mock.calls.find(
        ([u, init]) => String(u).endsWith('/admin/users/USR-1') && (init as RequestInit | undefined)?.method === 'PATCH',
      );
      expect(patchCall).toBeDefined();
    });
    const patchCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/users/USR-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    const body = JSON.parse(String((patchCall[1] as RequestInit).body)) as Record<string, unknown>;
    expect(body).toEqual({ row_version: 1, status: 'active' });

    await waitFor(() => {
      const refreshedRow = screen.getByRole('row', { name: /alice@example\.com/ });
      expect(within(refreshedRow).getByText('Active')).toBeInTheDocument();
    });
  });

  it('deactivate requires confirmation before sending anything', async () => {
    const f = routedFetch();
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByText('alice@example.com');
    const row = screen.getByRole('row', { name: /alice@example\.com/ });
    fireEvent.click(within(row).getByRole('button', { name: 'Deactivate' }));

    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('TERMINAL');
    expect(dialog).toHaveTextContent('cannot be undone');

    expect(
      f.mock.calls.some(
        ([u, init]) => String(u).endsWith('/admin/users/USR-1') && (init as RequestInit | undefined)?.method === 'PATCH',
      ),
    ).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Deactivate' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const patchCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/users/USR-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    const body = JSON.parse(String((patchCall[1] as RequestInit).body)) as Record<string, unknown>;
    expect(body).toEqual({ row_version: 1, status: 'deactivated' });
  });

  it('on 409, refetches with a notice and does not retry the status change', async () => {
    const f = routedFetch({ statusConflict: true });
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByText('alice@example.com');
    const row = screen.getByRole('row', { name: /alice@example\.com/ });
    fireEvent.click(within(row).getByRole('button', { name: 'Activate' }));

    expect(await screen.findByText('Changed since you loaded it')).toBeInTheDocument();
    // Still Invited — the conflicting move was never applied.
    expect(screen.getByText('Invited')).toBeInTheDocument();

    const patchCalls = f.mock.calls.filter(
      ([u, init]) => String(u).endsWith('/admin/users/USR-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    );
    expect(patchCalls.length).toBe(1);
  });
});

describe('permission handling', () => {
  it('renders the clean permission-needed state on a 403, not a crash', async () => {
    vi.stubGlobal('fetch', routedFetch({ forbidden: true }));
    renderApp();

    expect(
      await screen.findByText('You need platform-admin permission to manage the user registry.'),
    ).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Create user' })).toBeDisabled();
  });
});
