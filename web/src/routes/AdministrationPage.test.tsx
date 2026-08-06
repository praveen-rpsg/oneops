import { screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setToken } from '../auth';

/** Minimal unsigned JWT — mirrors auth.test.tsx's own helper. */
function jwt(claims: Record<string, unknown>) {
  const b64 = (o: unknown) => btoa(JSON.stringify(o)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
  return `${b64({ alg: 'RS256' })}.${b64(claims)}.sig`;
}

function mockFetch() {
  return vi.fn().mockImplementation(async (url: string) => {
    if (String(url).includes('/auth/config')) {
      return { ok: true, status: 200, json: async () => ({ auth_enabled: false }) };
    }
    return { ok: true, status: 200, json: async () => ({ items: [] }) };
  });
}

beforeEach(() => {
  window.history.replaceState(null, '', '/administration');
  setToken(null);
});
afterEach(() => vi.unstubAllGlobals());

describe('Administration page — auth-disabled (local dev)', () => {
  it('shows the graceful no-token notice instead of crashing or blanking', async () => {
    vi.stubGlobal('fetch', mockFetch());
    renderApp();

    expect(await screen.findByRole('heading', { name: 'Administration' })).toBeInTheDocument();
    expect(
      await screen.findByText(/Running with authentication disabled \(local development\)/),
    ).toBeInTheDocument();
  });

  it('still renders the static roles & permissions matrix with no token', async () => {
    vi.stubGlobal('fetch', mockFetch());
    renderApp();

    expect(await screen.findByRole('heading', { name: /Roles & permissions/ })).toBeInTheDocument();
    const table = await screen.findByRole('table', { name: /OneOps roles and the permissions/ });
    expect(within(table).getByText('oneops-reader')).toBeInTheDocument();
    expect(within(table).getByText('oneops-editor')).toBeInTheDocument();
    expect(within(table).getByText('oneops-admin')).toBeInTheDocument();
    expect(within(table).getByText('oneops-platform-admin')).toBeInTheDocument();
    expect(await screen.findByText(/roles are assigned by your identity provider/i)).toBeInTheDocument();
  });
});

describe('Administration page — signed-in session', () => {
  it('shows the subject, roles and effective permissions from the token', async () => {
    vi.stubGlobal('fetch', mockFetch());
    setToken(jwt({ sub: 'U-1', preferred_username: 'priya', roles: ['oneops-editor'] }));
    renderApp();

    expect(await screen.findByRole('heading', { name: 'Who am I' })).toBeInTheDocument();
    // Several strings below are shared with the roles & permissions matrix
    // below it (a role name is both a "Who am I" badge and a matrix row; a
    // permission label is both a badge and a matrix column header), and
    // TopNavigation additionally renders "priya" more than once (responsive
    // breakpoint variants) — the same reason auth.test.tsx asserts presence
    // via getAllByText rather than a single-node findByText.
    await waitFor(() => expect(screen.getAllByText('priya').length).toBeGreaterThan(0));
    await waitFor(() => expect(screen.getAllByText('oneops-editor').length).toBeGreaterThan(0));
    await waitFor(() => expect(screen.getAllByText(/Read \(Tenant\)/).length).toBeGreaterThan(0));
    await waitFor(() => expect(screen.getAllByText(/Write \(Tenant\)/).length).toBeGreaterThan(0));
    expect(screen.queryByText(/Running with authentication disabled/)).not.toBeInTheDocument();
  });

  it('degrades to an explicit empty state when the token carries no roles claim', async () => {
    vi.stubGlobal('fetch', mockFetch());
    setToken(jwt({ sub: 'U-2' }));
    renderApp();

    expect(await screen.findByText('No roles claimed by this token.')).toBeInTheDocument();
    expect(await screen.findByText('None.')).toBeInTheDocument();
  });
});
