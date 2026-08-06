import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { AlertRuleDTO } from '../alertRules';

const rule = (over: Partial<AlertRuleDTO> = {}): AlertRuleDTO => ({
  rule_id: 'RULE-CPU',
  asset_id: 'AST-WEB-1',
  metric: 'cpu_utilization',
  comparator: 'gt',
  threshold: 90,
  for_duration_seconds: 300,
  severity: 'warning',
  symptom_class: 'resource',
  enabled: true,
  last_state: 'ok',
  flap_dwell_seconds: 60,
  row_version: 1,
  created_at: '2026-08-06T08:00:00Z',
  updated_at: '2026-08-06T08:00:00Z',
  ...over,
});

const firing = rule({
  rule_id: 'RULE-DISK',
  asset_id: 'AST-DB-1',
  metric: 'disk_free_bytes',
  comparator: 'lt',
  threshold: 1_000_000,
  severity: 'critical',
  symptom_class: 'availability',
  last_state: 'firing',
  last_transition_at: '2026-08-06T09:00:00Z',
});

interface Fixture {
  list?: unknown;
  detail?: Record<string, AlertRuleDTO>;
}

function routedFetch(fx: Fixture = {}) {
  return vi.fn().mockImplementation(async (url: string) => {
    const u = String(url);
    const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    const detailMatch = u.match(/admin\/alert-rules\/([^/?]+)/);
    if (detailMatch) {
      const body = fx.detail?.[detailMatch[1]];
      if (!body) return { ok: false, status: 404, json: async () => ({ title: 'not found', status: 404 }) };
      return ok(body);
    }

    if (u.includes('/admin/alert-rules')) {
      if (fx.list === 'ERROR') {
        return { ok: false, status: 500, json: async () => ({ title: 'internal error', status: 500, detail: 'boom' }) };
      }
      return ok(fx.list ?? { items: [] });
    }

    return ok({});
  });
}

beforeEach(() => window.history.replaceState(null, '', '/alerts'));
afterEach(() => vi.unstubAllGlobals());

describe('alerts board', () => {
  it('renders every rule from a mocked list', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule(), firing] } }));
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Alerts/ })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: /AST-WEB-1 · cpu_utilization/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /AST-DB-1 · disk_free_bytes/ })).toBeInTheDocument();
    expect(screen.getByText('2 of 2 rules')).toBeInTheDocument();
  });

  it('filters by state', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule(), firing] } }));
    const { container } = renderApp();

    await screen.findByRole('button', { name: /AST-WEB-1 · cpu_utilization/ });

    // Cloudscape's Select is only reliably driven through its own test-utils
    // wrapper in jsdom (the dropdown list is mounted but ARIA-hidden until
    // opened via the trigger's onMouseDown handler, which raw fireEvent
    // click/keyDown does not reproduce).
    const stateSelect = createWrapper(container).findAllSelects()[0];
    act(() => stateSelect.openDropdown());
    act(() => stateSelect.selectOptionByValue('firing'));

    await waitFor(() =>
      expect(screen.queryByRole('button', { name: /AST-WEB-1 · cpu_utilization/ })).not.toBeInTheDocument(),
    );
    expect(screen.getByRole('button', { name: /AST-DB-1 · disk_free_bytes/ })).toBeInTheDocument();
    expect(screen.getByText('1 of 2 rules')).toBeInTheDocument();
  });

  it('opens a rule detail in the split panel on click', async () => {
    vi.stubGlobal(
      'fetch',
      routedFetch({ list: { items: [firing] }, detail: { 'RULE-DISK': firing } }),
    );
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: /AST-DB-1 · disk_free_bytes/ }));

    await waitFor(() => expect(screen.getByText('< 1000000')).toBeInTheDocument());
    expect(screen.getAllByText('Availability').length).toBeGreaterThan(0);
  });

  it('shows an explicit empty state with no rules', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] } }));
    renderApp();

    expect(await screen.findByText('No alert rules')).toBeInTheDocument();
    expect(screen.getByText(/No alert rules have been registered/)).toBeInTheDocument();
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
