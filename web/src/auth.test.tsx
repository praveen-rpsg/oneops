import { screen, waitFor } from '@testing-library/react';
import { renderApp } from './test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { getSubject, getToken, setToken } from './auth';
import { listArtifacts } from './api';

/** Minimal unsigned JWT — the console reads claims for display only; the server verifies. */
function jwt(claims: Record<string, unknown>) {
  const b64 = (o: unknown) =>
    btoa(JSON.stringify(o)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
  return `${b64({ alg: 'RS256' })}.${b64(claims)}.sig`;
}

function mockFetch(authConfig: Record<string, unknown>, rest: unknown = { items: [] }) {
  return vi.fn().mockImplementation(async (url: string) => {
    if (String(url).includes('/auth/config')) {
      return { ok: true, status: 200, json: async () => authConfig };
    }
    return { ok: true, status: 200, json: async () => rest };
  });
}

beforeEach(() => {
  window.history.replaceState(null, '', '/');
  setToken(null);
  sessionStorage.clear();
});
afterEach(() => vi.unstubAllGlobals());

describe('authentication', () => {
  it('goes straight to the estate when the deployment has auth disabled', async () => {
    vi.stubGlobal('fetch', mockFetch({ auth_enabled: false }));
    renderApp();
    expect(await screen.findByText('No artifacts yet')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Sign in' })).not.toBeInTheDocument();
  });

  it('prompts for sign-in when auth is enabled and no session exists', async () => {
    vi.stubGlobal(
      'fetch',
      mockFetch({ auth_enabled: true, issuer: 'https://idp.example', client_id: 'oneops' }),
    );
    renderApp();
    expect(await screen.findByRole('button', { name: 'Sign in' })).toBeInTheDocument();
  });

  it('does not request data before a session exists', async () => {
    const f = mockFetch({ auth_enabled: true, issuer: 'https://idp.example', client_id: 'x' });
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByRole('button', { name: 'Sign in' });
    const dataCalls = f.mock.calls.map(String).filter((u) => u.includes('/v1/'));
    expect(dataCalls).toHaveLength(0);
  });

  it('reports a deployment with no identity provider configured', async () => {
    vi.stubGlobal('fetch', mockFetch({ auth_enabled: true })); // issuer/client_id absent
    renderApp();

    const button = await screen.findByRole('button', { name: 'Sign in' });
    button.click();

    expect(await screen.findByRole('alert')).toHaveTextContent(/no identity provider/i);
  });

  it('attaches the bearer token to API requests once signed in', async () => {
    const f = mockFetch({ auth_enabled: false });
    vi.stubGlobal('fetch', f);
    setToken(jwt({ sub: 'priya@partner.example' }));

    await listArtifacts({});

    const init = f.mock.calls.at(-1)?.[1] as { headers: Record<string, string> };
    expect(init.headers.Authorization).toBe(`Bearer ${getToken()}`);
  });

  it('sends no Authorization header when there is no session', async () => {
    const f = mockFetch({ auth_enabled: false });
    vi.stubGlobal('fetch', f);

    await listArtifacts({});

    const init = f.mock.calls.at(-1)?.[1] as { headers: Record<string, string> };
    expect(init.headers.Authorization).toBeUndefined();
  });

  it('shows who is signed in, preferring a human-readable claim', async () => {
    vi.stubGlobal('fetch', mockFetch({ auth_enabled: false }));
    setToken(jwt({ sub: 'abc-123', preferred_username: 'priya' }));
    expect(getSubject()).toBe('priya');

    renderApp();
    // TopNavigation renders each utility more than once (responsive breakpoint
    // variants); assert presence, not a DOM-node count that's an implementation detail.
    await waitFor(() => expect(screen.getAllByText('priya').length).toBeGreaterThan(0));
    expect(screen.getByRole('button', { name: 'Sign out' })).toBeInTheDocument();
  });

  it('rejects a callback whose state does not match (CSRF defence)', async () => {
    sessionStorage.setItem('oneops.pkce.verifier', 'v');
    sessionStorage.setItem('oneops.pkce.state', 'expected-state');
    window.history.replaceState(null, '', '/?code=abc&state=attacker-state');
    vi.stubGlobal(
      'fetch',
      mockFetch({ auth_enabled: true, issuer: 'https://idp.example', client_id: 'x' }),
    );

    renderApp();

    expect(await screen.findByRole('alert')).toHaveTextContent(/could not be verified/i);
  });
});
