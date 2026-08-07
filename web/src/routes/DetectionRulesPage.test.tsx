import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { IncidentDTO } from '../incidents';
import type { SecurityRuleDTO } from '../securityRules';

// Exercises E-SEC-UI.2's Detection rules screen: list + filter (GET
// /v1/admin/security-rules), "Create rule" (POST .../security-rules) with
// every enum field a Select constrained to the real backend value set, the
// linked-incident drill-down (current_incident_id), the explicit empty
// state, error/retry, and the 403-graceful permission state.

const rule = (over: Partial<SecurityRuleDTO> = {}): SecurityRuleDTO => ({
  rule_id: 'RULE-SCAN',
  asset_id: 'AST-WEB-1',
  observation_type: 'port_scan',
  min_severity: 'medium',
  window_seconds: 300,
  threshold_count: 5,
  incident_severity: 'high',
  enabled: true,
  last_state: 'ok',
  row_version: 1,
  created_at: '2026-08-06T08:00:00Z',
  updated_at: '2026-08-06T08:00:00Z',
  ...over,
});

const firing = rule({
  rule_id: 'RULE-MALWARE',
  asset_id: 'AST-DB-1',
  observation_type: 'malware_detected',
  min_severity: 'high',
  threshold_count: 1,
  incident_severity: 'critical',
  last_state: 'firing',
  last_transition_at: '2026-08-06T09:00:00Z',
  current_incident_id: 'INC-LINKED',
});

const linkedIncident: IncidentDTO = {
  incident_id: 'INC-LINKED',
  title: 'Malware detected on AST-DB-1',
  description: 'malware_detected breached threshold.',
  severity: 'critical',
  status: 'open',
  source: 'manual',
  asset_id: 'AST-DB-1',
  row_version: 1,
  created_at: '2026-08-06T09:00:00Z',
  updated_at: '2026-08-06T09:00:00Z',
  is_root: true,
};

interface Fixture {
  list?: unknown;
  detail?: Record<string, SecurityRuleDTO>;
  incidentDetail?: Record<string, IncidentDTO>;
  /** 'ERROR' makes POST /v1/admin/security-rules fail 422, as a bad/out-of-range value would. */
  create?: 'ERROR';
  /** GET /admin/security-rules answers 403 — not a tenant admin. */
  forbidden?: boolean;
}

function routedFetch(fx: Fixture = {}) {
  const created: Record<string, SecurityRuleDTO> = {};
  return vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url);
    const method = init?.method ?? 'GET';
    const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
    const fail = (status: number, body: Record<string, unknown>) => ({ ok: false, status, json: async () => ({ status, ...body }) });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    if (method === 'POST' && /\/admin\/security-rules$/.test(u)) {
      if (fx.create === 'ERROR') {
        return fail(422, { title: 'validation failed', detail: 'threshold_count must be between 1 and 1000000' });
      }
      const body = JSON.parse(String(init?.body)) as Partial<SecurityRuleDTO>;
      const newRule = rule({
        rule_id: 'RULE-NEW',
        asset_id: body.asset_id ?? '',
        observation_type: body.observation_type ?? '',
        min_severity: body.min_severity ?? 'info',
        window_seconds: body.window_seconds ?? 300,
        threshold_count: body.threshold_count ?? 1,
        incident_severity: body.incident_severity ?? 'low',
        enabled: body.enabled ?? true,
        last_state: 'ok',
        current_incident_id: undefined,
      });
      created[newRule.rule_id] = newRule;
      return ok(newRule, 201);
    }

    const detailMatch = u.match(/admin\/security-rules\/([^/?]+)/);
    if (detailMatch) {
      const id = detailMatch[1];
      const body = fx.detail?.[id] ?? created[id];
      if (!body) return fail(404, { title: 'not found' });
      return ok(body);
    }

    if (u.includes('/incidents/') && u.includes('/timeline')) return ok({ items: [] });

    const incidentDetailMatch = u.match(/admin\/incidents\/([^/?]+)/);
    if (incidentDetailMatch) {
      const id = incidentDetailMatch[1];
      const body = fx.incidentDetail?.[id];
      if (!body) return fail(404, { title: 'not found' });
      return ok(body);
    }

    if (u.includes('/admin/security-rules')) {
      if (fx.forbidden) return fail(403, { title: 'forbidden', detail: 'missing permission: admin' });
      if (fx.list === 'ERROR') return fail(500, { title: 'internal error', detail: 'boom' });
      return ok(fx.list ?? { items: [] });
    }

    return ok({});
  });
}

beforeEach(() => window.history.replaceState(null, '', '/security/detection-rules'));
afterEach(() => vi.unstubAllGlobals());

describe('detection rules board', () => {
  it('renders every rule from a mocked list', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule(), firing] } }));
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Detection rules/ })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: /AST-WEB-1 · port_scan/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /AST-DB-1 · malware_detected/ })).toBeInTheDocument();
    expect(screen.getByText('2 of 2 rules')).toBeInTheDocument();
  });

  it('filters by state', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule(), firing] } }));
    const { container } = renderApp();

    await screen.findByRole('button', { name: /AST-WEB-1 · port_scan/ });

    const stateSelect = createWrapper(container).findAllSelects()[0];
    act(() => stateSelect.openDropdown());
    act(() => stateSelect.selectOptionByValue('firing'));

    await waitFor(() => expect(screen.queryByRole('button', { name: /AST-WEB-1 · port_scan/ })).not.toBeInTheDocument());
    expect(screen.getByRole('button', { name: /AST-DB-1 · malware_detected/ })).toBeInTheDocument();
    expect(screen.getByText('1 of 2 rules')).toBeInTheDocument();
  });

  it('opens a rule detail in the split panel on click', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [firing] }, detail: { 'RULE-MALWARE': firing } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: /AST-DB-1 · malware_detected/ }));

    await waitFor(() => expect(screen.getByText('1 in 300s')).toBeInTheDocument());
    expect(screen.getAllByText('Critical').length).toBeGreaterThan(0);
  });

  it('renders a linked-incident link when current_incident_id is set, and an em dash otherwise', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule(), firing] } }));
    renderApp();

    await screen.findByRole('button', { name: /AST-WEB-1 · port_scan/ });

    const unlinkedRow = screen.getByRole('row', { name: /AST-WEB-1 · port_scan/ });
    expect(within(unlinkedRow).getByText('—')).toBeInTheDocument();

    const linkedRow = screen.getByRole('row', { name: /AST-DB-1 · malware_detected/ });
    expect(within(linkedRow).getByRole('button', { name: 'INC-LINKED' })).toBeInTheDocument();
  });

  it('opens the linked incident in the split panel when its link is clicked', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [firing] }, incidentDetail: { 'INC-LINKED': linkedIncident } }));
    renderApp();

    const linkedRow = await screen.findByRole('row', { name: /AST-DB-1 · malware_detected/ });
    fireEvent.click(within(linkedRow).getByRole('button', { name: 'INC-LINKED' }));

    expect(await screen.findByRole('heading', { name: 'Incident INC-LINKED' })).toBeInTheDocument();
    expect(await screen.findByText('malware_detected breached threshold.')).toBeInTheDocument();
  });

  it('shows an explicit empty state with no rules', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] } }));
    renderApp();

    expect(await screen.findByText('No detection rules')).toBeInTheDocument();
    expect(screen.getByText(/No detection rules have been registered/)).toBeInTheDocument();
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

  it('renders the clean permission-needed state on a 403, not a crash', async () => {
    vi.stubGlobal('fetch', routedFetch({ forbidden: true }));
    renderApp();

    expect(
      await screen.findByText('You need tenant-admin permission to manage detection rules.'),
    ).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });
});

describe('create detection rule', () => {
  it('is disabled until the required fields are filled', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule()] } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
  });

  it('constrains min severity/incident severity to the real backend enum values', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule()] } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    const minSeveritySelect = createWrapper(dialog).findAllSelects()[0];
    act(() => minSeveritySelect.openDropdown());
    const minSeverityValues = minSeveritySelect
      .findDropdown()
      .findOptions()
      .map((o) => o.getElement().textContent);
    expect(minSeverityValues.join(' ')).toMatch(/Info.*Low.*Medium.*High.*Critical/s);
    act(() => minSeveritySelect.selectOptionByValue('high'));

    const incidentSeveritySelect = createWrapper(dialog).findAllSelects()[1];
    act(() => incidentSeveritySelect.openDropdown());
    const incidentSeverityValues = incidentSeveritySelect
      .findDropdown()
      .findOptions()
      .map((o) => o.getElement().textContent);
    expect(incidentSeverityValues.join(' ')).toMatch(/Critical.*High.*Medium.*Low/s);

    expect(dialog).toBeInTheDocument();
  });

  it('posts the exact create body and opens the new rule', async () => {
    const f = routedFetch({ list: { items: [rule()] } });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Asset ID'), { target: { value: 'AST-NEW' } });
    fireEvent.change(within(dialog).getByLabelText('Observation type'), { target: { value: 'brute_force' } });
    fireEvent.change(within(dialog).getByLabelText('Threshold count'), { target: { value: '10' } });

    const incidentSeveritySelect = createWrapper(dialog).findAllSelects()[1];
    act(() => incidentSeveritySelect.openDropdown());
    act(() => incidentSeveritySelect.selectOptionByValue('critical'));

    const listCallsBefore = f.mock.calls.filter(([u]) => String(u).includes('/admin/security-rules?')).length;

    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const createCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/security-rules') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    expect(JSON.parse(String((createCall[1] as RequestInit).body))).toEqual({
      asset_id: 'AST-NEW',
      observation_type: 'brute_force',
      min_severity: 'info',
      window_seconds: 300,
      threshold_count: 10,
      incident_severity: 'critical',
      enabled: true,
    });

    await waitFor(() =>
      expect(f.mock.calls.filter(([u]) => String(u).includes('/admin/security-rules?')).length).toBeGreaterThan(listCallsBefore),
    );
    expect(await screen.findByRole('heading', { name: 'AST-NEW · brute_force' })).toBeInTheDocument();
  });

  it('surfaces a validation error instead of closing the dialog', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule()] }, create: 'ERROR' }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Asset ID'), { target: { value: 'AST-BAD' } });
    fireEvent.change(within(dialog).getByLabelText('Observation type'), { target: { value: 'bad_type' } });
    fireEvent.change(within(dialog).getByLabelText('Threshold count'), { target: { value: '999999' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('threshold_count must be between');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('rejects an illegal observation_type before ever sending a request', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule()] } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Asset ID'), { target: { value: 'AST-BAD' } });
    fireEvent.change(within(dialog).getByLabelText('Observation type'), { target: { value: 'Not Valid!' } });
    fireEvent.change(within(dialog).getByLabelText('Threshold count'), { target: { value: '5' } });

    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
    expect(within(dialog).getByText(/lower-case snake_case/)).toBeInTheDocument();
  });
});
