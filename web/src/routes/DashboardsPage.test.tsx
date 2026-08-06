import { screen, waitFor } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import { fireEvent } from '@testing-library/react';
import type { IncidentTrendsResponse } from '../dashboards';
import type { AssetGraph } from '../assetGraph';

const TRENDS: IncidentTrendsResponse = {
  from: '2026-08-05T12:00:00Z',
  to: '2026-08-06T12:00:00Z',
  bucket: 'hour',
  points: [
    {
      bucket_start: '2026-08-06T10:00:00Z',
      opened_total: 3,
      opened_by_severity: { critical: 1, high: 1, medium: 1, low: 0 },
      opened_by_source: { manual: 2, alert: 1 },
      resolved_total: 1,
    },
    {
      bucket_start: '2026-08-06T11:00:00Z',
      opened_total: 0,
      opened_by_severity: { critical: 0, high: 0, medium: 0, low: 0 },
      opened_by_source: { manual: 0, alert: 0 },
      resolved_total: 0,
    },
  ],
};

const GRAPH: AssetGraph = {
  nodes: [{ asset_id: 'AST-1', name: 'web-1', type: 'server', status: 'active', environment: 'production', criticality: 'high' }],
  edges: [],
  truncated: false,
};

interface Fixture {
  trends?: unknown;
  graph?: unknown;
  telemetry?: unknown;
}

function routedFetch(fx: Fixture = {}) {
  return vi.fn().mockImplementation(async (url: string) => {
    const u = String(url);
    const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });
    const err = () => ({ ok: false, status: 500, json: async () => ({ title: 'internal error', status: 500, detail: 'boom' }) });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });
    if (u.includes('/admin/dashboards/incident-trends')) {
      return fx.trends === 'ERROR' ? err() : ok(fx.trends ?? TRENDS);
    }
    if (u.includes('/admin/assets/graph')) return ok(fx.graph ?? GRAPH);
    if (u.includes('/admin/telemetry')) return ok(fx.telemetry ?? { items: [] });
    return ok({});
  });
}

beforeEach(() => window.history.replaceState(null, '', '/dashboards'));
afterEach(() => vi.unstubAllGlobals());

describe('dashboards page', () => {
  it('renders the incident-volume chart series from a mocked trends response', async () => {
    vi.stubGlobal('fetch', routedFetch());
    const { container } = renderApp();

    expect(await screen.findByRole('heading', { name: 'Dashboards' })).toBeInTheDocument();
    expect(await screen.findByRole('heading', { name: 'Incident volume' })).toBeInTheDocument();

    const chart = createWrapper(container).findMixedLineBarChart();
    expect(chart).not.toBeNull();
    const legendText = chart!
      .findLegend()!
      .findItems()
      .map((i) => i.getElement().textContent?.trim());
    expect(legendText).toEqual(expect.arrayContaining(['Critical', 'High', 'Medium', 'Low', 'Resolved']));
  });

  it('defaults to Last 24h/bucket=hour and refetches with bucket=day on a wider range', async () => {
    const fetchMock = routedFetch();
    vi.stubGlobal('fetch', fetchMock);
    const { container } = renderApp();

    await screen.findByRole('heading', { name: 'Incident volume' });

    const trendCalls = () => fetchMock.mock.calls.filter((c) => String(c[0]).includes('/dashboards/incident-trends'));
    await waitFor(() => expect(trendCalls().length).toBeGreaterThanOrEqual(1));
    expect(String(trendCalls()[0][0])).toContain('bucket=hour');

    const callsBefore = trendCalls().length;
    const segmented = createWrapper(container).findSegmentedControl()!;
    segmented.findSegmentById('7d')!.click();

    await waitFor(() => expect(trendCalls().length).toBeGreaterThan(callsBefore));
    expect(String(trendCalls()[trendCalls().length - 1][0])).toContain('bucket=day');
  });

  it('renders a clean no-data state for empty telemetry, never a crash', async () => {
    vi.stubGlobal('fetch', routedFetch({ telemetry: { items: [] } }));
    renderApp();

    await screen.findByRole('heading', { name: 'Telemetry trend' });
    expect(await screen.findByText('No telemetry samples')).toBeInTheDocument();
  });

  it('renders telemetry samples when present', async () => {
    vi.stubGlobal(
      'fetch',
      routedFetch({
        telemetry: {
          items: [
            { asset_id: 'AST-1', metric: 'cpu_utilization', value: 42, timestamp: '2026-08-06T11:55:00Z', labels: {} },
          ],
        },
      }),
    );
    const { container } = renderApp();

    await screen.findByRole('heading', { name: 'Telemetry trend' });
    await waitFor(() => expect(createWrapper(container).findLineChart()!.findSeries().length).toBeGreaterThan(0));
  });

  it('shows an error banner on trends failure and lets the operator retry', async () => {
    const fetchMock = routedFetch({ trends: 'ERROR' });
    vi.stubGlobal('fetch', fetchMock);
    renderApp();

    expect(await screen.findByRole('alert')).toHaveTextContent('internal error');

    const callsBefore = fetchMock.mock.calls.length;
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));

    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(callsBefore));
  });
});
