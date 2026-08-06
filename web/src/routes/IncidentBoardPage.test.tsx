import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { IncidentDTO, IncidentEventDTO } from '../incidents';

const incident = (over: Partial<IncidentDTO> = {}): IncidentDTO => ({
  incident_id: 'INC-STANDALONE',
  title: 'Disk full on batch-worker-2',
  description: 'Batch worker local disk exhausted.',
  severity: 'medium',
  status: 'open',
  source: 'manual',
  row_version: 1,
  created_at: '2026-08-06T10:00:00Z',
  updated_at: '2026-08-06T10:00:00Z',
  is_root: true,
  ...over,
});

const root = incident({
  incident_id: 'INC-ROOT',
  title: 'Primary database down',
  description: 'Root cause: primary DB instance unreachable.',
  severity: 'critical',
  status: 'open',
  source: 'alert',
  asset_id: 'AST-DB-1',
  created_at: '2026-08-06T08:00:00Z',
  is_root: true,
});

const collateral = incident({
  incident_id: 'INC-COLLATERAL',
  title: 'API 5xx spike',
  description: 'Downstream of the primary database outage.',
  severity: 'high',
  status: 'open',
  source: 'alert',
  root_incident_id: 'INC-ROOT',
  is_root: false,
  created_at: '2026-08-06T08:05:00Z',
});

const standalone = incident();

const rootTimeline: IncidentEventDTO[] = [
  {
    event_id: 'EV-1',
    incident_id: 'INC-ROOT',
    kind: 'created',
    actor: 'alert-correlator',
    row_version: 1,
    occurred_at: '2026-08-06T08:00:00Z',
  },
];

interface Fixture {
  list?: unknown;
  detail?: Record<string, IncidentDTO>;
  timeline?: Record<string, { items: IncidentEventDTO[] }>;
}

function routedFetch(fx: Fixture = {}) {
  return vi.fn().mockImplementation(async (url: string) => {
    const u = String(url);
    const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    const timelineMatch = u.match(/incidents\/([^/?]+)\/timeline/);
    if (timelineMatch) {
      return ok(fx.timeline?.[timelineMatch[1]] ?? { items: [] });
    }

    const detailMatch = u.match(/admin\/incidents\/([^/?]+)/);
    if (detailMatch) {
      const body = fx.detail?.[detailMatch[1]];
      if (!body) return { ok: false, status: 404, json: async () => ({ title: 'not found', status: 404 }) };
      return ok(body);
    }

    if (u.includes('/admin/incidents')) {
      if (fx.list === 'ERROR') {
        return { ok: false, status: 500, json: async () => ({ title: 'internal error', status: 500, detail: 'boom' }) };
      }
      return ok(fx.list ?? { items: [] });
    }

    return ok({});
  });
}

beforeEach(() => window.history.replaceState(null, '', '/incidents'));
afterEach(() => vi.unstubAllGlobals());

describe('incident board', () => {
  it('groups a root with its nested collateral, and renders a standalone incident flat', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [root, collateral, standalone] } }));
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Incidents/ })).toBeInTheDocument();

    // The root and its collateral are both visible (default-expanded groups).
    expect(await screen.findByRole('button', { name: 'Primary database down' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'API 5xx spike' })).toBeInTheDocument();
    expect(screen.getByText(/collateral of INC-ROOT/)).toBeInTheDocument();

    // The standalone incident renders as its own top-level row, not nested.
    expect(screen.getByRole('button', { name: 'Disk full on batch-worker-2' })).toBeInTheDocument();

    expect(screen.getByText('3 open incidents')).toBeInTheDocument();
  });

  it('defaults the fetch to status=open, bounded at the documented cap', async () => {
    const fetchMock = routedFetch({ list: { items: [root, collateral, standalone] } });
    vi.stubGlobal('fetch', fetchMock);
    renderApp();

    await screen.findByRole('heading', { name: /^Incidents/ });
    const listCall = String(fetchMock.mock.calls.map((c) => String(c[0])).find((u) => u.includes('/admin/incidents?')));
    expect(listCall).toContain('status=open');
    expect(listCall).toContain('limit=100');
  });

  it('opens an incident detail + timeline in the split panel on click', async () => {
    vi.stubGlobal(
      'fetch',
      routedFetch({
        list: { items: [root, collateral, standalone] },
        detail: { 'INC-ROOT': root },
        timeline: { 'INC-ROOT': { items: rootTimeline } },
      }),
    );
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Primary database down' }));

    await waitFor(() => expect(screen.getByText('Root cause: primary DB instance unreachable.')).toBeInTheDocument());
    expect(screen.getByText('alert-correlator')).toBeInTheDocument();
    expect(screen.queryByText('No timeline events yet.')).not.toBeInTheDocument();
  });

  it('shows an explicit empty state for the active filter, with a way to widen it', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] } }));
    renderApp();

    expect(await screen.findByText(/No incidents with status "Open"/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Show all statuses' }));

    await waitFor(() => expect(screen.getByText(/No incidents\.?$/)).toBeInTheDocument());
  });

  it('shows an error banner on failure and lets the operator retry', async () => {
    const fetchMock = routedFetch({ list: 'ERROR' });
    vi.stubGlobal('fetch', fetchMock);
    renderApp();

    expect(await screen.findByRole('alert')).toHaveTextContent('internal error');

    const callsBefore = fetchMock.mock.calls.length;
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));

    await vi.waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(callsBefore));
  });
});
