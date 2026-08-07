import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { AssetGraph } from '../assetGraph';
import type { SecurityObservationDTO } from '../securityObservations';

// Exercises E-SEC-UI.4's Observations screen: a bounded RANGE query (GET
// /v1/admin/security-observations, asset_id/observation_type/from/to ALL
// required — querySecurityObservations, handlers_security_observations.go:
// 151-212) with client-side severity filtering, the attribute-chip summary +
// its detail panel, the explicit empty/not-yet-queried states, error/retry,
// the 403-graceful permission state, and — the quality bar this screen must
// clear — NO mutate control anywhere.

const GRAPH: AssetGraph = {
  nodes: [{ asset_id: 'AST-1', name: 'web-1', type: 'server', status: 'active', environment: 'production', criticality: 'high' }],
  edges: [],
  truncated: false,
};

const observation = (over: Partial<SecurityObservationDTO> = {}): SecurityObservationDTO => ({
  asset_id: 'AST-1',
  observation_type: 'port_scan',
  source: 'wazuh',
  severity: 'high',
  observed_at: '2026-08-06T10:00:00Z',
  attributes: { actor: '203.0.113.5', target_port: '22' },
  ...over,
});

interface Fixture {
  graph?: unknown;
  observations?: unknown;
  /** GET /admin/security-observations answers 403 — not a tenant admin. */
  forbidden?: boolean;
}

function routedFetch(fx: Fixture = {}) {
  return vi.fn().mockImplementation(async (url: string) => {
    const u = String(url);
    const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
    const fail = (status: number, body: Record<string, unknown>) => ({ ok: false, status, json: async () => ({ status, ...body }) });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });
    if (u.includes('/admin/assets/graph')) return ok(fx.graph ?? GRAPH);
    if (u.includes('/admin/security-observations')) {
      if (fx.forbidden) return fail(403, { title: 'forbidden', detail: 'missing permission: admin' });
      if (fx.observations === 'ERROR') return fail(500, { title: 'internal error', detail: 'boom' });
      return ok(fx.observations ?? { items: [] });
    }
    return ok({});
  });
}

/** Selects AST-1 (auto-selected by default), types a valid observation_type, and clicks Load. */
async function loadObservations(observationType = 'port_scan') {
  await screen.findByRole('heading', { name: 'Observations' });
  fireEvent.change(screen.getByLabelText('Observation type'), { target: { value: observationType } });
  fireEvent.click(screen.getByRole('button', { name: 'Load' }));
}

beforeEach(() => window.history.replaceState(null, '', '/security/observations'));
afterEach(() => vi.unstubAllGlobals());

describe('observations page', () => {
  it('renders the read-only heading and description', async () => {
    vi.stubGlobal('fetch', routedFetch());
    renderApp();

    expect(await screen.findByRole('heading', { name: 'Observations' })).toBeInTheDocument();
    expect(screen.getByText(/Read-only — no create, edit or delete here/)).toBeInTheDocument();
  });

  it('shows a not-yet-queried empty state and disables Load until asset and observation type are set', async () => {
    vi.stubGlobal('fetch', routedFetch());
    renderApp();

    await screen.findByRole('heading', { name: 'Observations' });
    expect(screen.getByRole('button', { name: 'Load' })).toBeDisabled();
    expect(await screen.findByText('Choose an asset and an observation type, then Load, to query this append-only fact table.')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Observation type'), { target: { value: 'port_scan' } });
    expect(screen.getByRole('button', { name: 'Load' })).toBeEnabled();
  });

  it('rejects an invalid observation_type before ever enabling Load', async () => {
    vi.stubGlobal('fetch', routedFetch());
    renderApp();
    await screen.findByRole('heading', { name: 'Observations' });

    fireEvent.change(screen.getByLabelText('Observation type'), { target: { value: 'Not Valid!' } });

    expect(screen.getByRole('button', { name: 'Load' })).toBeDisabled();
    expect(screen.getByText(/lower-case snake_case/)).toBeInTheDocument();
  });

  it('queries with asset_id, observation_type and a default 24h range, and renders the result table', async () => {
    const f = routedFetch({ observations: { items: [observation()] } });
    vi.stubGlobal('fetch', f);
    renderApp();

    await loadObservations();

    expect(await screen.findByRole('row', { name: /port_scan/ })).toBeInTheDocument();

    const queryCall = f.mock.calls.find(([u]) => String(u).includes('/admin/security-observations?'))!;
    const params = new URL(String(queryCall[0]), 'http://localhost').searchParams;
    expect(params.get('asset_id')).toBe('AST-1');
    expect(params.get('observation_type')).toBe('port_scan');
    expect(params.get('from')).toBeTruthy();
    expect(params.get('to')).toBeTruthy();

    const row = screen.getByRole('row', { name: /port_scan/ });
    expect(within(row).getByText('wazuh')).toBeInTheDocument();
    expect(within(row).getByText('High')).toBeInTheDocument();
    expect(within(row).getByText('AST-1')).toBeInTheDocument();
    expect(within(row).getByText('actor=203.0.113.5')).toBeInTheDocument();
  });

  it('shows an explicit empty state when the query returns nothing', async () => {
    vi.stubGlobal('fetch', routedFetch({ observations: { items: [] } }));
    renderApp();

    await loadObservations();

    expect(
      await screen.findByText('No security observations were recorded for this asset and observation type in the selected window.'),
    ).toBeInTheDocument();
  });

  it('filters by severity client-side', async () => {
    vi.stubGlobal(
      'fetch',
      routedFetch({ observations: { items: [observation(), observation({ source: 'cloudtrail', severity: 'low' })] } }),
    );
    const { container } = renderApp();

    await loadObservations();
    await screen.findByText('2 of 2 observations for AST-1 · port_scan');

    // index 0 = Asset, index 1 = the SegmentedControl's own narrow-viewport
    // Select fallback (jsdom has no real width, so Cloudscape renders it as
    // a Select showing the selected range's label), index 2 = Severity.
    const severitySelect = createWrapper(container).findAllSelects()[2];
    act(() => severitySelect.openDropdown());
    act(() => severitySelect.selectOptionByValue('high'));

    await waitFor(() => expect(screen.getByText('1 of 2 observations for AST-1 · port_scan')).toBeInTheDocument());
    expect(screen.queryByText('cloudtrail')).not.toBeInTheDocument();
  });

  it('opens a detail panel with the full attribute set on row click, without a second fetch', async () => {
    const f = routedFetch({ observations: { items: [observation()] } });
    vi.stubGlobal('fetch', f);
    renderApp();

    await loadObservations();
    const callsBefore = f.mock.calls.length;

    fireEvent.click(await screen.findByRole('button', { name: /2026/ }));

    expect(await screen.findByText('target_port')).toBeInTheDocument();
    expect(screen.getByText('22')).toBeInTheDocument();
    expect(screen.getByText(/append-only fact table/)).toBeInTheDocument();
    expect(f.mock.calls.length).toBe(callsBefore);
  });

  it('shows a truncation banner when the page hits the default cap', async () => {
    const many = Array.from({ length: 500 }, (_, i) => observation({ observed_at: `2026-08-06T${String(10 + (i % 10)).padStart(2, '0')}:00:00Z`, source: `s-${i}` }));
    vi.stubGlobal('fetch', routedFetch({ observations: { items: many } }));
    renderApp();

    await loadObservations();

    expect(await screen.findByText(/Showing the most recent 500 observations/)).toBeInTheDocument();
  });

  it('shows an error banner on failure and lets the operator retry', async () => {
    const f = routedFetch({ observations: 'ERROR' });
    vi.stubGlobal('fetch', f);
    renderApp();

    await loadObservations();

    expect(await screen.findByRole('alert')).toHaveTextContent('internal error');

    const callsBefore = f.mock.calls.length;
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));

    await waitFor(() => expect(f.mock.calls.length).toBeGreaterThan(callsBefore));
  });

  it('renders the clean permission-needed state on a 403, not a crash', async () => {
    vi.stubGlobal('fetch', routedFetch({ forbidden: true }));
    renderApp();

    await loadObservations();

    expect(
      await screen.findByText('You need tenant-admin permission to view security observations.'),
    ).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('offers no mutate control anywhere on the screen', async () => {
    vi.stubGlobal('fetch', routedFetch({ observations: { items: [observation()] } }));
    renderApp();

    await loadObservations();
    await screen.findByRole('row', { name: /port_scan/ });

    expect(screen.queryByRole('button', { name: /create/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^edit$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^delete$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^enable$|^disable$/i })).not.toBeInTheDocument();
  });
});
