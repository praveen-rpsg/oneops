import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

// Exercises E-ID.5's invitee REDEEM screen (over E-ID.4b's
// POST /auth/invitations/redeem, ADR-IAC-004), and App.tsx's public-route
// bypass that makes it reachable at all: it must render for a visitor who is
// NOT signed in, without ever calling /auth/config or attaching a bearer
// token, prefill the token from ?token=, and collapse every failure cause
// into the one generic message the server is deliberately built to return.

function routedFetch(fx: { redeem?: 'ERROR' } = {}) {
  return vi.fn().mockImplementation(async (url: string) => {
    const u = String(url);
    const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
    const fail = (status: number, body: Record<string, unknown>) => ({
      ok: false,
      status,
      json: async () => ({ status, ...body }),
    });

    if (u.includes('/auth/invitations/redeem')) {
      if (fx.redeem === 'ERROR') {
        return fail(400, { title: 'bad request', detail: 'invitation is not redeemable' });
      }
      return ok({ organization: 'Acme Corp' });
    }

    // /auth/config and anything else this screen should never need to call.
    return fail(599, { title: 'unexpected call', detail: u });
  });
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('signed-out rendering', () => {
  beforeEach(() => {
    window.history.replaceState(null, '', '/redeem?token=abc123');
  });

  it('renders the redeem screen without any session, and never calls /auth/config', async () => {
    const f = routedFetch();
    vi.stubGlobal('fetch', f);
    renderApp();

    expect(await screen.findByLabelText('Invitation token')).toBeInTheDocument();
    expect(screen.queryByText('Sign in to OneOps')).not.toBeInTheDocument();

    expect(f.mock.calls.some(([u]) => String(u).includes('/auth/config'))).toBe(false);
  });

  it('pre-fills the token from ?token= and posts {token} with no Authorization header', async () => {
    const f = routedFetch();
    vi.stubGlobal('fetch', f);
    renderApp();

    const input = (await screen.findByLabelText('Invitation token')) as HTMLInputElement;
    expect(input.value).toBe('abc123');

    fireEvent.click(screen.getByRole('button', { name: 'Accept invitation' }));

    await screen.findByText(/joined/i);

    const redeemCall = f.mock.calls.find(([u]) => String(u).includes('/auth/invitations/redeem'))!;
    expect(JSON.parse(String((redeemCall[1] as RequestInit).body))).toEqual({ token: 'abc123' });
    expect((redeemCall[1] as RequestInit).headers).not.toHaveProperty('Authorization');
  });

  it('shows the organization and a sign-in CTA on success', async () => {
    vi.stubGlobal('fetch', routedFetch());
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Accept invitation' }));

    expect(await screen.findByText(/Acme Corp/)).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeInTheDocument();
  });

  it('shows the single generic failure message on a 400, never a distinguishing cause', async () => {
    vi.stubGlobal('fetch', routedFetch({ redeem: 'ERROR' }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Accept invitation' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('This invitation link is invalid or has expired.');
    expect(screen.queryByText(/invitation is not redeemable/)).not.toBeInTheDocument();
  });

  it('disables the accept button while the request is pending', async () => {
    let resolveRedeem: (v: { ok: boolean; status: number; json: () => Promise<unknown> }) => void = () => {};
    const pending = new Promise<{ ok: boolean; status: number; json: () => Promise<unknown> }>((resolve) => {
      resolveRedeem = resolve;
    });
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async (url: string) => {
        if (String(url).includes('/auth/invitations/redeem')) return pending;
        return { ok: false, status: 599, json: async () => ({}) };
      }),
    );
    renderApp();

    const button = await screen.findByRole('button', { name: 'Accept invitation' });
    fireEvent.click(button);

    await waitFor(() => expect(button).toBeDisabled());
    resolveRedeem({ ok: true, status: 200, json: async () => ({ organization: 'Acme Corp' }) });

    expect(await screen.findByText(/Acme Corp/)).toBeInTheDocument();
  });
});

describe('does not disturb the existing auth gate for other routes', () => {
  it('still shows SignIn for a normal route when signed out', async () => {
    window.history.replaceState(null, '', '/');
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async (url: string) => {
        if (String(url).includes('/auth/config')) {
          return { ok: true, status: 200, json: async () => ({ auth_enabled: true, issuer: 'https://idp.example', client_id: 'oneops-console' }) };
        }
        return { ok: false, status: 599, json: async () => ({}) };
      }),
    );
    renderApp();

    expect(await screen.findByText('Sign in to OneOps')).toBeInTheDocument();
  });

  it('renders RedeemPage at /redeem regardless of auth state, without requiring auth config', async () => {
    window.history.replaceState(null, '', '/redeem');
    vi.stubGlobal(
      'fetch',
      vi.fn().mockImplementation(async () => ({ ok: false, status: 599, json: async () => ({}) })),
    );
    renderApp();

    expect(await screen.findByLabelText('Invitation token')).toBeInTheDocument();
  });
});
