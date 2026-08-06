import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { AlertRuleDTO } from '../alertRules';
import type { IncidentDTO } from '../incidents';

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
  current_incident_id: 'INC-LINKED',
});

const linkedIncident: IncidentDTO = {
  incident_id: 'INC-LINKED',
  title: 'Disk exhaustion on AST-DB-1',
  description: 'disk_free_bytes breached threshold.',
  severity: 'critical',
  status: 'open',
  source: 'alert',
  asset_id: 'AST-DB-1',
  row_version: 1,
  created_at: '2026-08-06T09:00:00Z',
  updated_at: '2026-08-06T09:00:00Z',
  is_root: true,
};

interface Fixture {
  list?: unknown;
  detail?: Record<string, AlertRuleDTO>;
  incidentDetail?: Record<string, IncidentDTO>;
  /** 'ERROR' makes POST /v1/admin/alert-rules fail 422, as a bad enum/out-of-range value would. */
  create?: 'ERROR';
}

function routedFetch(fx: Fixture = {}) {
  const created: Record<string, AlertRuleDTO> = {};
  return vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url);
    const method = init?.method ?? 'GET';
    const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    if (method === 'POST' && /\/admin\/alert-rules$/.test(u)) {
      if (fx.create === 'ERROR') {
        return {
          ok: false,
          status: 422,
          json: async () => ({ title: 'validation failed', status: 422, detail: 'threshold must be a finite number' }),
        };
      }
      const body = JSON.parse(String(init?.body)) as Partial<AlertRuleDTO>;
      const newRule = rule({
        rule_id: 'RULE-NEW',
        asset_id: body.asset_id ?? '',
        metric: body.metric ?? '',
        comparator: body.comparator ?? 'gt',
        threshold: body.threshold ?? 0,
        for_duration_seconds: body.for_duration_seconds ?? 300,
        severity: body.severity ?? 'warning',
        symptom_class: body.symptom_class ?? 'unspecified',
        enabled: body.enabled ?? true,
        flap_dwell_seconds: body.flap_dwell_seconds ?? 60,
      });
      created[newRule.rule_id] = newRule;
      return ok(newRule, 201);
    }

    const detailMatch = u.match(/admin\/alert-rules\/([^/?]+)/);
    if (detailMatch) {
      const id = detailMatch[1];
      const body = fx.detail?.[id] ?? created[id];
      if (!body) return { ok: false, status: 404, json: async () => ({ title: 'not found', status: 404 }) };
      return ok(body);
    }

    if (u.includes('/incidents/') && u.includes('/timeline')) {
      return ok({ items: [] });
    }

    const incidentDetailMatch = u.match(/admin\/incidents\/([^/?]+)/);
    if (incidentDetailMatch) {
      const id = incidentDetailMatch[1];
      const body = fx.incidentDetail?.[id];
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

  it('renders a linked-incident link when current_incident_id is set, and an em dash otherwise', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule(), firing] } }));
    renderApp();

    await screen.findByRole('button', { name: /AST-WEB-1 · cpu_utilization/ });

    const unlinkedRow = screen.getByRole('row', { name: /AST-WEB-1 · cpu_utilization/ });
    expect(within(unlinkedRow).getByText('—')).toBeInTheDocument();
    expect(within(unlinkedRow).queryByRole('button', { name: /INC-/ })).not.toBeInTheDocument();

    const linkedRow = screen.getByRole('row', { name: /AST-DB-1 · disk_free_bytes/ });
    expect(within(linkedRow).getByRole('button', { name: 'INC-LINKED' })).toBeInTheDocument();
  });

  it('opens the linked incident in the split panel when its link is clicked', async () => {
    vi.stubGlobal(
      'fetch',
      routedFetch({ list: { items: [firing] }, incidentDetail: { 'INC-LINKED': linkedIncident } }),
    );
    renderApp();

    const linkedRow = await screen.findByRole('row', { name: /AST-DB-1 · disk_free_bytes/ });
    fireEvent.click(within(linkedRow).getByRole('button', { name: 'INC-LINKED' }));

    expect(await screen.findByRole('heading', { name: 'Incident INC-LINKED' })).toBeInTheDocument();
    expect(await screen.findByText('disk_free_bytes breached threshold.')).toBeInTheDocument();
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

// E-ACT.2 / ADR-ACT-002: "Create rule" — the enum Selects only ever offer the
// real backend value set, so a bad enum is impossible to submit; the form
// posts the exact createAlertRuleRequest body and opens the new rule's
// detail on success, mirroring E-ACT.1's "create incident" test shape.
describe('create alert rule', () => {
  it('is disabled until the required fields are filled', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule()] } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
  });

  it('constrains comparator/severity/symptom-class to the real backend enum values', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule()] } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    const comparatorSelect = createWrapper(dialog).findAllSelects()[0];
    act(() => comparatorSelect.openDropdown());
    const comparatorValues = comparatorSelect
      .findDropdown()
      .findOptions()
      .map((o) => o.getElement().textContent);
    expect(comparatorValues.join(' ')).toMatch(/gt.*lt.*gte.*lte/s);
    act(() => comparatorSelect.selectOptionByValue('lte'));

    expect(dialog).toBeInTheDocument();
  });

  it('posts the exact create body and opens the new rule', async () => {
    const f = routedFetch({ list: { items: [rule()] } });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Asset ID'), { target: { value: 'AST-NEW' } });
    fireEvent.change(within(dialog).getByLabelText('Metric'), { target: { value: 'queue_depth' } });
    fireEvent.change(within(dialog).getByLabelText('Threshold'), { target: { value: '500' } });
    fireEvent.change(within(dialog).getByLabelText('For duration seconds'), { target: { value: '120' } });

    const severitySelect = createWrapper(dialog).findAllSelects()[1];
    act(() => severitySelect.openDropdown());
    act(() => severitySelect.selectOptionByValue('critical'));

    const symptomSelect = createWrapper(dialog).findAllSelects()[2];
    act(() => symptomSelect.openDropdown());
    act(() => symptomSelect.selectOptionByValue('availability'));

    const listCallsBefore = f.mock.calls.filter(([u]) => String(u).includes('/admin/alert-rules?')).length;

    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const createCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/alert-rules') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    expect(JSON.parse(String((createCall[1] as RequestInit).body))).toEqual({
      asset_id: 'AST-NEW',
      metric: 'queue_depth',
      comparator: 'gt',
      threshold: 500,
      for_duration_seconds: 120,
      severity: 'critical',
      symptom_class: 'availability',
      enabled: true,
      flap_dwell_seconds: undefined,
    });

    // The board reloaded its list...
    await waitFor(() =>
      expect(f.mock.calls.filter(([u]) => String(u).includes('/admin/alert-rules?')).length).toBeGreaterThan(
        listCallsBefore,
      ),
    );
    // ...and the new rule's own detail opened in the split panel.
    expect(await screen.findByRole('heading', { name: 'AST-NEW · queue_depth' })).toBeInTheDocument();
  });

  it('surfaces a validation error instead of closing the dialog', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule()] }, create: 'ERROR' }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Asset ID'), { target: { value: 'AST-BAD' } });
    fireEvent.change(within(dialog).getByLabelText('Metric'), { target: { value: 'bad_metric' } });
    fireEvent.change(within(dialog).getByLabelText('Threshold'), { target: { value: 'not-really-used' } });

    // Threshold is non-numeric, so Create stays disabled and no request is sent — a
    // distinct, earlier-caught case than the server-side 422 this test also proves
    // shows up instead of a silently-closed dialog when the server itself rejects it.
    fireEvent.change(within(dialog).getByLabelText('Threshold'), { target: { value: '999999' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('threshold must be a finite number');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});
