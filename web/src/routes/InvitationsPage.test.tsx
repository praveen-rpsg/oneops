import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { InvitationDTO } from '../invitations';

// Exercises E-ID.5's Invitations screen (admin side, over E-ID.4a's
// endpoints, ADR-IAC-003): create (POST /v1/admin/invitations, surfacing the
// one-time token + shareable /redeem link exactly once), list
// (GET /v1/admin/invitations — no org_id to resolve, unlike MembersPage),
// revoke (DELETE, confirm-then-delete-then-refetch, ADR-HARD-003's
// no-row_version delete, offered only on pending rows), and the 403-graceful
// permission state.

const invitation = (over: Partial<InvitationDTO> = {}): InvitationDTO => ({
  invitation_id: 'INV-1',
  org_id: 'ORG-1',
  email: 'alice@example.com',
  status: 'pending',
  expires_at: '2026-08-14T00:00:00Z',
  created_at: '2026-08-07T00:00:00Z',
  ...over,
});

const redeemed = invitation({ invitation_id: 'INV-2', email: 'bob@example.com', status: 'redeemed', redeemed_at: '2026-08-08T00:00:00Z' });
const revokedFixture = invitation({ invitation_id: 'INV-3', email: 'carol@example.com', status: 'revoked' });

interface Fixture {
  items?: InvitationDTO[];
  /** GET /admin/invitations answers 403 — not a tenant admin. */
  forbidden?: boolean;
  /** POST /v1/admin/invitations fails 422. */
  createError?: boolean;
  /** DELETE /v1/admin/invitations/{id} fails 409. */
  revokeError?: boolean;
}

function routedFetch(fx: Fixture = {}) {
  const items = [...(fx.items ?? [invitation(), redeemed, revokedFixture])];

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

    if (method === 'POST' && /\/admin\/invitations$/.test(u)) {
      if (fx.createError) return fail(422, { title: 'validation failed', detail: 'email: must not be empty' });
      const body = JSON.parse(String(init?.body)) as { email: string };
      const created = invitation({
        invitation_id: 'INV-NEW',
        email: body.email,
        status: 'pending',
      });
      items.push(created);
      return ok({ ...created, token: 'ttttttttttttttttttttttttttttttttttttttttttt' }, 201);
    }

    const idMatch = u.match(/\/admin\/invitations\/([^/?]+)$/);
    if (method === 'DELETE' && idMatch) {
      if (fx.revokeError) return fail(409, { title: 'conflict', detail: 'already redeemed' });
      const i = items.findIndex((it) => it.invitation_id === idMatch[1]);
      if (i >= 0) items[i] = { ...items[i], status: 'revoked' };
      return ok(items[i], 200);
    }

    if (u.includes('/admin/invitations')) {
      if (fx.forbidden) return fail(403, { title: 'forbidden', detail: 'missing permission: artifacts:admin' });
      return ok({ items });
    }

    return ok({});
  });
}

beforeEach(() => {
  window.history.replaceState(null, '', '/invitations');
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe('invitations list', () => {
  it('renders email, status and expires/created timestamps', async () => {
    vi.stubGlobal('fetch', routedFetch());
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Invitations/ })).toBeInTheDocument();
    expect(await screen.findByText('alice@example.com')).toBeInTheDocument();
    expect(screen.getByText('Pending')).toBeInTheDocument();
    expect(screen.getByText('Redeemed')).toBeInTheDocument();
    expect(screen.getByText('Revoked')).toBeInTheDocument();
  });

  it('offers Revoke only on a pending invitation', async () => {
    vi.stubGlobal('fetch', routedFetch());
    renderApp();

    await screen.findByText('alice@example.com');

    const pendingRow = screen.getByRole('row', { name: /alice@example\.com/ });
    expect(within(pendingRow).getByRole('button', { name: 'Revoke' })).toBeInTheDocument();

    const redeemedRow = screen.getByRole('row', { name: /bob@example\.com/ });
    expect(within(redeemedRow).queryByRole('button', { name: 'Revoke' })).not.toBeInTheDocument();

    const revokedRow = screen.getByRole('row', { name: /carol@example\.com/ });
    expect(within(revokedRow).queryByRole('button', { name: 'Revoke' })).not.toBeInTheDocument();
  });
});

describe('create invitation', () => {
  it('posts the exact {email} body and surfaces the one-time token + shareable redeem link', async () => {
    const f = routedFetch();
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByText('alice@example.com');
    fireEvent.click(screen.getByRole('button', { name: 'Invite by email' }));
    const createDialog = await screen.findByRole('dialog');
    fireEvent.change(within(createDialog).getByLabelText('Email'), { target: { value: 'new@example.com' } });
    fireEvent.click(within(createDialog).getByRole('button', { name: 'Invite' }));

    // The create dialog is replaced by the one-time token reveal — never both, never neither.
    // (`findByRole('dialog')` alone would resolve instantly against the still-open create
    // dialog, since one is already mounted; wait for reveal-only text instead.)
    await screen.findByText(/shown once/i);
    const revealDialog = screen.getByRole('dialog');

    const postCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/invitations') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    const body = JSON.parse(String((postCall[1] as RequestInit).body)) as Record<string, unknown>;
    expect(body).toEqual({ email: 'new@example.com' });
    expect(within(revealDialog).getByText('ttttttttttttttttttttttttttttttttttttttttttt')).toBeInTheDocument();
    expect(within(revealDialog).getByText(/\/redeem\?token=ttttttttttttttttttttttttttttttttttttttttttt/)).toBeInTheDocument();

    fireEvent.click(within(revealDialog).getByRole('button', { name: 'Done' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(await screen.findByText('new@example.com')).toBeInTheDocument();
  });

  it('surfaces a server validation error and keeps the create dialog open (no token reveal)', async () => {
    vi.stubGlobal('fetch', routedFetch({ createError: true }));
    renderApp();

    await screen.findByText('alice@example.com');
    fireEvent.click(screen.getByRole('button', { name: 'Invite by email' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Email'), { target: { value: 'new@example.com' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Invite' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('must not be empty');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.queryByText(/shown once/i)).not.toBeInTheDocument();
  });
});

describe('revoke invitation', () => {
  it('confirms, then deletes with no body, then refetches the list', async () => {
    const f = routedFetch();
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByText('alice@example.com');
    const listCallsBefore = f.mock.calls.filter(([u]) => String(u).includes('/admin/invitations?')).length;

    const row = screen.getByRole('row', { name: /alice@example\.com/ });
    fireEvent.click(within(row).getByRole('button', { name: 'Revoke' }));

    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('alice@example.com');
    expect(f.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Revoke invitation' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const deleteCall = f.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')!;
    expect(String(deleteCall[0])).toContain('/admin/invitations/INV-1');
    expect(deleteCall[1]).not.toHaveProperty('body');

    await waitFor(() =>
      expect(f.mock.calls.filter(([u]) => String(u).includes('/admin/invitations?')).length).toBeGreaterThan(
        listCallsBefore,
      ),
    );
  });

  it('surfaces a revoke error inline rather than failing silently', async () => {
    vi.stubGlobal('fetch', routedFetch({ revokeError: true }));
    renderApp();

    await screen.findByText('alice@example.com');
    const row = screen.getByRole('row', { name: /alice@example\.com/ });
    fireEvent.click(within(row).getByRole('button', { name: 'Revoke' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Revoke invitation' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('already redeemed');
    // Still shown as pending — no silent status flip on a failed revoke.
    expect(screen.getByText('Pending')).toBeInTheDocument();
  });
});

describe('permission handling', () => {
  it('renders the clean permission-needed state on a 403, not a crash', async () => {
    vi.stubGlobal('fetch', routedFetch({ forbidden: true }));
    renderApp();

    expect(
      await screen.findByText('You need tenant-admin permission to manage invitations.'),
    ).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Invite by email' })).toBeDisabled();
  });
});
