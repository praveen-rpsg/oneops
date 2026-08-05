import { screen, waitFor, within } from '@testing-library/react';
import { renderApp } from './test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { ConfigObject } from './api';

const artifact = (over: Partial<ConfigObject> = {}): ConfigObject => ({
  cfg_id: '01KY9G4NW92Q2XTHK83N2SX1BD',
  artifact: 'OneOps-Constitution-Volume-I.md',
  version: '1.0.0',
  role: 'constitution',
  lifecycle: 'ratified',
  retention_class: 'current_baseline',
  authority: 'active',
  row_version: 1,
  created_at: '2026-07-24T12:00:00Z',
  updated_at: '2026-07-24T12:00:00Z',
  ...over,
});

/**
 * The console fetches /auth/config before any data request. Route it here so the
 * fixtures mirror the real request sequence; everything else gets `body`.
 */
function mockJSON(body: unknown, ok = true, status = 200) {
  return vi.fn().mockImplementation(async (url: string) => {
    if (String(url).includes('/auth/config')) {
      return { ok: true, status: 200, json: async () => ({ auth_enabled: false }) };
    }
    return { ok, status, json: async () => body };
  });
}

/** First call to the artifacts endpoint, ignoring the auth-config preflight. */
const artifactsCall = (m: ReturnType<typeof vi.fn>) =>
  String(m.mock.calls.map(String).find((u) => u.includes('/v1/artifacts')) ?? '');

beforeEach(() => {
  window.history.replaceState(null, '', '/');
});
afterEach(() => vi.unstubAllGlobals());

describe('estate list', () => {
  it('renders artifacts with their authority', async () => {
    vi.stubGlobal('fetch', mockJSON({ items: [artifact()] }));
    renderApp();

    expect(await screen.findByText('OneOps-Constitution-Volume-I.md')).toBeInTheDocument();

    // Scope to the table: the filter selects also contain "Active"/"Constitution".
    const row = screen.getByRole('row', { name: /OneOps-Constitution-Volume-I/ });
    expect(within(row).getByText('Active')).toBeInTheDocument();
    expect(within(row).getByText('Constitution')).toBeInTheDocument();
  });

  it('distinguishes an empty estate from filters matching nothing', async () => {
    vi.stubGlobal('fetch', mockJSON({ items: [] }));
    const { unmount } = renderApp();
    expect(await screen.findByText('No artifacts yet')).toBeInTheDocument();
    unmount();

    window.history.replaceState(null, '', '/?role=audit');
    renderApp();
    expect(await screen.findByText('No artifacts match these filters')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Clear filters' })).toBeInTheDocument();
  });

  it('renders the server problem detail verbatim', async () => {
    vi.stubGlobal(
      'fetch',
      mockJSON(
        {
          title: 'validation failed',
          status: 422,
          detail: 'validation failed on "lifecycle": at inception must be draft or in_progress (INT-3)',
          instance: 'req-abc123',
        },
        false,
        422,
      ),
    );
    renderApp();

    expect(await screen.findByRole('alert')).toBeInTheDocument();
    expect(screen.getByText(/at inception must be draft or in_progress/)).toBeInTheDocument();
    expect(screen.getByText('req-abc123')).toBeInTheDocument();
  });

  it('sends the active filters to the API and reflects them in the URL', async () => {
    const fetchMock = mockJSON({ items: [artifact()] });
    vi.stubGlobal('fetch', fetchMock);
    window.history.replaceState(null, '', '/?authority=active&role=constitution');
    renderApp();

    await waitFor(() => expect(artifactsCall(fetchMock)).toContain('/v1/artifacts'));
    const url = artifactsCall(fetchMock);
    expect(url).toContain('authority=active');
    expect(url).toContain('role=constitution');
    expect(window.location.search).toContain('authority=active');
  });

  it('offers Load more only when the API returns a cursor', async () => {
    vi.stubGlobal('fetch', mockJSON({ items: [artifact()], next_cursor: 'c2' }));
    renderApp();
    expect(await screen.findByRole('button', { name: 'Load more' })).toBeInTheDocument();
  });
});
