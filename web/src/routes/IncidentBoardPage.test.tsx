import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
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
  /** 'ERROR' makes POST /v1/admin/incidents fail 422, as a bad severity/empty title would. */
  create?: 'ERROR';
}

function routedFetch(fx: Fixture = {}) {
  const created: Record<string, IncidentDTO> = {};
  return vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url);
    const method = init?.method ?? 'GET';
    const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    if (u.includes('/admin/users')) return ok({ items: [] });

    if (method === 'POST' && /\/admin\/incidents$/.test(u)) {
      if (fx.create === 'ERROR') {
        return { ok: false, status: 422, json: async () => ({ title: 'validation failed', status: 422, detail: 'title must not be empty' }) };
      }
      const body = JSON.parse(String(init?.body)) as { title: string; description?: string; severity: string; asset_id?: string };
      const inc = incident({
        incident_id: 'INC-NEW',
        title: body.title,
        description: body.description ?? '',
        severity: body.severity as IncidentDTO['severity'],
        asset_id: body.asset_id,
        row_version: 1,
      });
      created[inc.incident_id] = inc;
      return ok(inc, 201);
    }

    const timelineMatch = u.match(/incidents\/([^/?]+)\/timeline/);
    if (timelineMatch) {
      return ok(fx.timeline?.[timelineMatch[1]] ?? { items: [] });
    }

    const detailMatch = u.match(/admin\/incidents\/([^/?]+)/);
    if (detailMatch) {
      const id = detailMatch[1];
      const body = fx.detail?.[id] ?? created[id];
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

// E-ACT.1 / ADR-ACT-001: "Create incident" — the fourth write action this
// story adds. Title required, everything else optional; posts to
// createIncident and, on success, refetches the board and opens the new
// incident's detail rather than leaving the operator to find it themselves.
describe('create incident', () => {
  it('is disabled until a title is entered', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [standalone] } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create incident' }));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
  });

  it('posts the form and opens the new incident', async () => {
    const f = routedFetch({ list: { items: [standalone] } });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create incident' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Title'), { target: { value: 'New API outage' } });

    const severitySelect = createWrapper(dialog).findAllSelects()[0];
    act(() => severitySelect.openDropdown());
    act(() => severitySelect.selectOptionByValue('critical'));

    const listCallsBefore = f.mock.calls.filter(([u]) => String(u).includes('/admin/incidents?')).length;

    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const createCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/incidents') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    expect(JSON.parse(String((createCall[1] as RequestInit).body))).toEqual({
      title: 'New API outage',
      description: undefined,
      severity: 'critical',
      asset_id: undefined,
    });

    // The board reloaded its list...
    await waitFor(() =>
      expect(f.mock.calls.filter(([u]) => String(u).includes('/admin/incidents?')).length).toBeGreaterThan(
        listCallsBefore,
      ),
    );
    // ...and the new incident's own detail opened in the split panel.
    expect(await screen.findByRole('heading', { name: 'New API outage' })).toBeInTheDocument();
  });

  it('surfaces a validation error instead of closing the dialog', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [standalone] }, create: 'ERROR' }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create incident' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Title'), { target: { value: 'x' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('title must not be empty');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});
