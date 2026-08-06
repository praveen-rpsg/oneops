import { fireEvent, screen } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { NOCOverview } from '../noc';

const overview = (over: Partial<NOCOverview> = {}): NOCOverview => ({
  incidents: {
    open_total: 3,
    by_status: { open: 1, acknowledged: 1, investigating: 1 },
    by_severity: { critical: 1, high: 1, medium: 1, low: 0 },
    grouped: { root_count: 2, collateral_count: 1 },
  },
  alerts: {
    firing_total: 2,
    by_severity: { critical: 1, warning: 1, info: 0 },
  },
  assets: {
    stale: 4,
    orphaned_assets: 1,
    orphaned_business_services: 0,
    incomplete: 2,
  },
  on_call: [
    { schedule_id: 'SCH1', schedule_name: 'Primary on-call', user_id: 'U1', display_name: 'Ada Lovelace' },
  ],
  escalations: { active_total: 1 },
  generated_at: '2026-08-06T12:00:00Z',
  ...over,
});

const EMPTY_OVERVIEW: NOCOverview = {
  incidents: {
    open_total: 0,
    by_status: { open: 0, acknowledged: 0, investigating: 0 },
    by_severity: { critical: 0, high: 0, medium: 0, low: 0 },
    grouped: { root_count: 0, collateral_count: 0 },
  },
  alerts: { firing_total: 0, by_severity: { critical: 0, warning: 0, info: 0 } },
  assets: { stale: 0, orphaned_assets: 0, orphaned_business_services: 0, incomplete: 0 },
  on_call: [],
  escalations: { active_total: 0 },
  generated_at: '2026-08-06T12:00:00Z',
};

/** The console fetches /auth/config before any data request; everything else is routed by URL fragment. */
function routedFetch(over: Record<string, unknown> = {}) {
  return vi.fn().mockImplementation(async (url: string) => {
    if (String(url).includes('/auth/config')) {
      return { ok: true, status: 200, json: async () => ({ auth_enabled: false }) };
    }
    for (const [frag, body] of Object.entries(over)) {
      if (String(url).includes(frag)) {
        if (body === 'ERROR') {
          return {
            ok: false,
            status: 500,
            json: async () => ({ title: 'internal error', status: 500, detail: 'boom' }),
          };
        }
        return { ok: true, status: 200, json: async () => body };
      }
    }
    return { ok: true, status: 200, json: async () => ({}) };
  });
}

beforeEach(() => window.history.replaceState(null, '', '/noc'));
afterEach(() => vi.unstubAllGlobals());

describe('NOC overview page', () => {
  it('renders every widget from the overview response', async () => {
    vi.stubGlobal('fetch', routedFetch({ '/v1/admin/noc/overview': overview() }));
    renderApp();

    expect(await screen.findByRole('heading', { name: 'NOC / Overview' })).toBeInTheDocument();

    // The h1 above renders unconditionally on first paint, before the mocked
    // fetch's promise chain has necessarily settled — awaiting it is not
    // sufficient to guarantee the data-dependent widgets below have rendered
    // yet under load. `findByRole` (polling) rather than a bare `getByRole`
    // is required here for the same reason line 75 needs `await`.
    expect(await screen.findByRole('heading', { name: 'Incidents' })).toBeInTheDocument();
    expect(screen.getByText('Open total')).toBeInTheDocument();
    // open_total = 3 is the only field equal to 3 in this fixture.
    expect(screen.getByText('3')).toBeInTheDocument();
    expect(screen.getByText('Root incidents')).toBeInTheDocument();

    expect(screen.getByRole('heading', { name: 'Alerts firing' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Asset health' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'On call now' })).toBeInTheDocument();
    expect(screen.getByText('Primary on-call')).toBeInTheDocument();
    expect(screen.getByText('Ada Lovelace')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Escalations' })).toBeInTheDocument();

    expect(screen.getByText(/Last updated/)).toBeInTheDocument();
  });

  it('renders every widget cleanly at zero — no null/NaN, an explicit empty state', async () => {
    vi.stubGlobal('fetch', routedFetch({ '/v1/admin/noc/overview': EMPTY_OVERVIEW }));
    renderApp();

    // "NOC / Overview" is the page's static h1 — it renders on first paint,
    // before the mocked overview fetch has necessarily settled. The widgets
    // below only exist once `overview` is set, so the first one asserted on
    // must be polled for (`findBy*`); the rest are populated by that same
    // state update and safe to read synchronously once it's found.
    await screen.findByRole('heading', { name: 'NOC / Overview' });

    expect(await screen.findByText('No active on-call schedules.')).toBeInTheDocument();
    expect(screen.getByText('All clear')).toBeInTheDocument();
    expect(screen.queryByText('NaN')).not.toBeInTheDocument();
    expect(screen.queryByText('null')).not.toBeInTheDocument();
    expect(screen.queryByText('undefined')).not.toBeInTheDocument();
  });

  it('shows an error banner on failure and lets the operator retry', async () => {
    const fetchMock = routedFetch({ '/v1/admin/noc/overview': 'ERROR' });
    vi.stubGlobal('fetch', fetchMock);
    renderApp();

    expect(await screen.findByRole('alert')).toHaveTextContent('internal error');

    const callsBefore = fetchMock.mock.calls.length;
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));

    await vi.waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(callsBefore));
  });

  it('refetches on manual refresh without duplicating the poll timer', async () => {
    const fetchMock = routedFetch({ '/v1/admin/noc/overview': overview() });
    vi.stubGlobal('fetch', fetchMock);
    renderApp();

    await screen.findByRole('heading', { name: 'NOC / Overview' });
    const callsAfterLoad = fetchMock.mock.calls.filter((c) => String(c[0]).includes('/noc/overview')).length;
    expect(callsAfterLoad).toBe(1);

    fireEvent.click(screen.getByRole('button', { name: 'Refresh' }));

    await vi.waitFor(() => {
      const calls = fetchMock.mock.calls.filter((c) => String(c[0]).includes('/noc/overview')).length;
      expect(calls).toBe(2);
    });
  });
});
