import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { AssetGraph } from '../assetGraph';
import type { AssetHealthReport } from '../assetGraph';
import type { IncidentDTO } from '../incidents';

const emptyHealth: AssetHealthReport = {
  stale_after: '720h',
  stale: { count: 0, samples: [] },
  orphaned_assets: { count: 0, samples: [] },
  orphaned_business_services: { count: 0, samples: [] },
  incomplete: { count: 0, samples: [] },
};

const threeTierGraph: AssetGraph = {
  nodes: [
    { asset_id: 'AST-WEB', name: 'web-frontend', type: 'service', status: 'active', environment: 'production', criticality: 'high' },
    { asset_id: 'AST-API', name: 'api-gateway', type: 'service', status: 'active', environment: 'production', criticality: 'critical' },
    { asset_id: 'AST-DB', name: 'primary-db', type: 'database', status: 'active', environment: 'production', criticality: 'critical' },
  ],
  edges: [
    { from_asset_id: 'AST-WEB', to_asset_id: 'AST-API', type: 'depends_on' },
    { from_asset_id: 'AST-API', to_asset_id: 'AST-DB', type: 'depends_on' },
  ],
  truncated: false,
};

const cyclicGraph: AssetGraph = {
  nodes: [
    { asset_id: 'AST-A', name: 'node-a', type: 'service', status: 'active', environment: 'production', criticality: 'medium' },
    { asset_id: 'AST-B', name: 'node-b', type: 'service', status: 'active', environment: 'production', criticality: 'medium' },
    { asset_id: 'AST-C', name: 'node-c', type: 'service', status: 'active', environment: 'production', criticality: 'medium' },
  ],
  edges: [
    { from_asset_id: 'AST-A', to_asset_id: 'AST-B', type: 'depends_on' },
    { from_asset_id: 'AST-B', to_asset_id: 'AST-C', type: 'depends_on' },
    { from_asset_id: 'AST-C', to_asset_id: 'AST-A', type: 'depends_on' },
  ],
  truncated: false,
};

interface Fixture {
  graph?: unknown;
  incidents?: unknown;
  health?: unknown;
}

function routedFetch(fx: Fixture = {}) {
  return vi.fn().mockImplementation(async (url: string) => {
    const u = String(url);
    const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    if (u.includes('/admin/assets/graph')) {
      if (fx.graph === 'ERROR') return { ok: false, status: 500, json: async () => ({ title: 'internal error', status: 500, detail: 'boom' }) };
      return ok(fx.graph ?? { nodes: [], edges: [], truncated: false });
    }
    if (u.includes('/admin/assets/health')) {
      if (fx.health === 'ERROR') return { ok: false, status: 500, json: async () => ({ title: 'boom', status: 500 }) };
      return ok(fx.health ?? emptyHealth);
    }
    if (u.includes('/admin/incidents')) {
      if (fx.incidents === 'ERROR') return { ok: false, status: 500, json: async () => ({ title: 'boom', status: 500 }) };
      return ok(fx.incidents ?? { items: [] });
    }

    return ok({});
  });
}

beforeEach(() => window.history.replaceState(null, '', '/topology'));
afterEach(() => vi.unstubAllGlobals());

describe('topology map', () => {
  it('renders every node and edge from the graph endpoint', async () => {
    vi.stubGlobal('fetch', routedFetch({ graph: threeTierGraph }));
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Topology/ })).toBeInTheDocument();
    expect(await screen.findByRole('img', { name: /asset dependency topology map/i })).toBeInTheDocument();

    expect(screen.getByText('web-frontend')).toBeInTheDocument();
    expect(screen.getByText('api-gateway')).toBeInTheDocument();
    expect(screen.getByText('primary-db')).toBeInTheDocument();

    // Every edge is drawn as a <title>-annotated <line> (nested inside a
    // <g>, not a direct <svg> child, so getByText — not getByTitle, which
    // testing-library only resolves for `svg > title` — is what finds it).
    expect(screen.getByText('AST-WEB depends_on AST-API')).toBeInTheDocument();
    expect(screen.getByText('AST-API depends_on AST-DB')).toBeInTheDocument();
  });

  it('colors a node red when it carries an open incident', async () => {
    const withIncident: IncidentDTO = {
      incident_id: 'INC-1',
      title: 'API errors',
      description: 'desc',
      severity: 'critical',
      status: 'open',
      source: 'alert',
      asset_id: 'AST-API',
      row_version: 1,
      created_at: '2026-08-06T00:00:00Z',
      updated_at: '2026-08-06T00:00:00Z',
      is_root: true,
    };
    vi.stubGlobal('fetch', routedFetch({ graph: threeTierGraph, incidents: { items: [withIncident] } }));
    renderApp();

    fireEvent.click(await screen.findByText('api-gateway'));

    await waitFor(() => expect(screen.getByText('1 open incident')).toBeInTheDocument());
  });

  it('colors a node amber when it carries a health issue but no open incident', async () => {
    const health: AssetHealthReport = {
      ...emptyHealth,
      stale: { count: 1, samples: [{ asset_id: 'AST-DB', type: 'database', name: 'primary-db' }] },
    };
    vi.stubGlobal('fetch', routedFetch({ graph: threeTierGraph, health }));
    renderApp();

    fireEvent.click(await screen.findByText('primary-db'));

    await waitFor(() => expect(screen.getByText('Stale')).toBeInTheDocument());
    expect(screen.getByText('None')).toBeInTheDocument(); // no open incidents on this node
  });

  it('renders a cyclic graph without hanging, marking the closing edge distinctly', async () => {
    vi.stubGlobal('fetch', routedFetch({ graph: cyclicGraph }));
    const start = Date.now();
    renderApp();

    expect(await screen.findByText('node-a')).toBeInTheDocument();
    expect(screen.getByText('node-b')).toBeInTheDocument();
    expect(screen.getByText('node-c')).toBeInTheDocument();
    expect(Date.now() - start).toBeLessThan(5000);

    // All three edges of the cycle are drawn — nothing was dropped or hung.
    expect(screen.getByText('AST-A depends_on AST-B')).toBeInTheDocument();
    expect(screen.getByText('AST-B depends_on AST-C')).toBeInTheDocument();
    expect(screen.getByText('AST-C depends_on AST-A (cycle)')).toBeInTheDocument();
  });

  it('shows a truncation alert when the graph endpoint reports truncated:true', async () => {
    vi.stubGlobal('fetch', routedFetch({ graph: { ...threeTierGraph, truncated: true } }));
    renderApp();

    expect(await screen.findByText(/Showing a partial graph/)).toBeInTheDocument();
  });

  it('shows a clean empty state when the tenant has no assets', async () => {
    vi.stubGlobal('fetch', routedFetch({ graph: { nodes: [], edges: [], truncated: false } }));
    renderApp();

    expect(await screen.findByText('No assets in the CMDB yet')).toBeInTheDocument();
  });

  it('shows an error banner on graph fetch failure and lets the operator retry', async () => {
    const fetchMock = routedFetch({ graph: 'ERROR' });
    vi.stubGlobal('fetch', fetchMock);
    renderApp();

    expect(await screen.findByRole('alert')).toHaveTextContent('internal error');

    const callsBefore = fetchMock.mock.calls.length;
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));

    await vi.waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(callsBefore));
  });

  it('still renders the graph when overlay sources fail', async () => {
    vi.stubGlobal('fetch', routedFetch({ graph: threeTierGraph, incidents: 'ERROR', health: 'ERROR' }));
    renderApp();

    expect(await screen.findByText('web-frontend')).toBeInTheDocument();
    expect(screen.getByText('api-gateway')).toBeInTheDocument();
    expect(screen.getByText('primary-db')).toBeInTheDocument();
  });

  it('refreshes the split panel once the overlay resolves, for a node clicked before it does (ADR-HARD-002)', async () => {
    const withIncident: IncidentDTO = {
      incident_id: 'INC-1',
      title: 'API errors',
      description: 'desc',
      severity: 'critical',
      status: 'open',
      source: 'alert',
      asset_id: 'AST-API',
      row_version: 1,
      created_at: '2026-08-06T00:00:00Z',
      updated_at: '2026-08-06T00:00:00Z',
      is_root: true,
    };

    // The incidents fetch is held open under test control, so the click
    // below is guaranteed to land before it resolves — no timing luck.
    let resolveIncidents!: (body: unknown) => void;
    const incidentsGate = new Promise<unknown>((resolve) => {
      resolveIncidents = resolve;
    });

    const fetchMock = vi.fn().mockImplementation(async (url: string) => {
      const u = String(url);
      const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });
      if (u.includes('/auth/config')) return ok({ auth_enabled: false });
      if (u.includes('/admin/assets/graph')) return ok(threeTierGraph);
      if (u.includes('/admin/assets/health')) return ok(emptyHealth);
      if (u.includes('/admin/incidents')) {
        const body = await incidentsGate;
        return ok(body);
      }
      return ok({});
    });
    vi.stubGlobal('fetch', fetchMock);
    renderApp();

    // Click the node while the incidents fetch (which will report the open
    // incident) is still in flight.
    fireEvent.click(await screen.findByText('api-gateway'));

    // Right after the click the overlay has not resolved yet: the panel must
    // not claim there is no incident (that would be the stale/incomplete
    // snapshot this story fixes) — it shows a neutral/loading state instead.
    expect(screen.queryByText('1 open incident')).not.toBeInTheDocument();

    // Now let the held-open incidents fetch resolve.
    resolveIncidents({ items: [withIncident] });

    // Without any re-click, the still-open panel for the still-selected node
    // updates to the resolved overlay.
    await waitFor(() => expect(screen.getByText('1 open incident')).toBeInTheDocument());
  });
});
