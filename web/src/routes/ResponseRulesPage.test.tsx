import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { SecurityResponseRuleDTO } from '../securityResponseRules';

// Exercises E-SEC-UI.4's Response rules screen: list + filter (GET
// /v1/admin/security-response-rules), "Create rule" (POST .../security-response-rules)
// with min_severity/action_type both Selects constrained to the real backend
// value sets — action_type NEVER offering anything outside the SAFE
// allowlist (http|notification) — the SAFE-boundary copy, the explicit empty
// state, error/retry, and the 403-graceful permission state.

const rule = (over: Partial<SecurityResponseRuleDTO> = {}): SecurityResponseRuleDTO => ({
  rule_id: 'RESP-1',
  name: 'Notify SOC on critical malware',
  min_severity: 'high',
  asset_id: 'AST-DB-1',
  action_type: 'http',
  action_config: { url: 'https://example.com/hooks/security' },
  enabled: true,
  row_version: 1,
  created_at: '2026-08-06T08:00:00Z',
  updated_at: '2026-08-06T08:00:00Z',
  ...over,
});

const anyAssetRule = rule({
  rule_id: 'RESP-2',
  name: 'Notify on any critical incident',
  min_severity: 'critical',
  asset_id: undefined,
  action_type: 'notification',
  action_config: { channel: '#security-alerts' },
});

interface Fixture {
  list?: unknown;
  detail?: Record<string, SecurityResponseRuleDTO>;
  /** 'ERROR' makes POST /v1/admin/security-response-rules fail 422, as a bad/out-of-range value would. */
  create?: 'ERROR';
  /** GET /admin/security-response-rules answers 403 — not a tenant admin. */
  forbidden?: boolean;
}

function routedFetch(fx: Fixture = {}) {
  const created: Record<string, SecurityResponseRuleDTO> = {};
  return vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url);
    const method = init?.method ?? 'GET';
    const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
    const fail = (status: number, body: Record<string, unknown>) => ({ ok: false, status, json: async () => ({ status, ...body }) });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    if (method === 'POST' && /\/admin\/security-response-rules$/.test(u)) {
      if (fx.create === 'ERROR') {
        return fail(422, { title: 'validation failed', detail: 'action_type must be one of the SAFE actions: http, notification' });
      }
      const body = JSON.parse(String(init?.body)) as Partial<SecurityResponseRuleDTO>;
      const newRule = rule({
        rule_id: 'RESP-NEW',
        name: body.name ?? '',
        min_severity: body.min_severity ?? 'high',
        asset_id: body.asset_id,
        action_type: body.action_type ?? 'http',
        action_config: body.action_config ?? {},
        enabled: body.enabled ?? true,
      });
      created[newRule.rule_id] = newRule;
      return ok(newRule, 201);
    }

    const detailMatch = u.match(/admin\/security-response-rules\/([^/?]+)/);
    if (detailMatch) {
      const id = detailMatch[1];
      const body = fx.detail?.[id] ?? created[id];
      if (!body) return fail(404, { title: 'not found' });
      return ok(body);
    }

    if (u.includes('/admin/security-response-rules')) {
      if (fx.forbidden) return fail(403, { title: 'forbidden', detail: 'missing permission: admin' });
      if (fx.list === 'ERROR') return fail(500, { title: 'internal error', detail: 'boom' });
      return ok(fx.list ?? { items: [] });
    }

    return ok({});
  });
}

beforeEach(() => window.history.replaceState(null, '', '/security/response-rules'));
afterEach(() => vi.unstubAllGlobals());

describe('response rules board', () => {
  it('renders every rule from a mocked list', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule(), anyAssetRule] } }));
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Response rules/ })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Notify SOC on critical malware' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Notify on any critical incident' })).toBeInTheDocument();
    expect(screen.getByText('2 of 2 rules')).toBeInTheDocument();
  });

  it('conveys the SAFE boundary in the board copy', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] } }));
    renderApp();

    expect(await screen.findByText(/SAFE-only response automation/)).toBeInTheDocument();
    expect(screen.getByText('SAFE boundary')).toBeInTheDocument();
    expect(screen.getByText(/Destructive or autonomous response is not available/)).toBeInTheDocument();
  });

  it('renders "Any asset" for a rule with no asset_id, and the asset_id otherwise', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule(), anyAssetRule] } }));
    renderApp();

    await screen.findByRole('button', { name: 'Notify SOC on critical malware' });
    const scopedRow = screen.getByRole('row', { name: /Notify SOC on critical malware/ });
    expect(within(scopedRow).getByText('AST-DB-1')).toBeInTheDocument();

    const anyRow = screen.getByRole('row', { name: /Notify on any critical incident/ });
    expect(within(anyRow).getByText('Any asset')).toBeInTheDocument();
  });

  it('filters by action type', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule(), anyAssetRule] } }));
    const { container } = renderApp();

    await screen.findByRole('button', { name: 'Notify SOC on critical malware' });

    const actionTypeSelect = createWrapper(container).findAllSelects()[0];
    act(() => actionTypeSelect.openDropdown());
    act(() => actionTypeSelect.selectOptionByValue('notification'));

    await waitFor(() => expect(screen.queryByRole('button', { name: 'Notify SOC on critical malware' })).not.toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'Notify on any critical incident' })).toBeInTheDocument();
    expect(screen.getByText('1 of 2 rules')).toBeInTheDocument();
  });

  it('opens a rule detail in the split panel on click', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [rule()] }, detail: { 'RESP-1': rule() } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Notify SOC on critical malware' }));

    await waitFor(() => expect(screen.getByText('Http')).toBeInTheDocument());
    expect(screen.getAllByText('High').length).toBeGreaterThan(0);
  });

  it('shows an explicit empty state with no rules', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] } }));
    renderApp();

    expect(await screen.findByText('No response rules')).toBeInTheDocument();
    expect(screen.getByText(/No response-automation rules have been registered/)).toBeInTheDocument();
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

    expect(await screen.findByText('You need tenant-admin permission to manage response rules.')).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });
});

describe('create response rule', () => {
  it('is disabled until the required fields are filled', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
  });

  it('offers ONLY the SAFE action types (http, notification) — never command or any destructive verb', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    const actionTypeSelect = createWrapper(dialog).findAllSelects()[1];
    act(() => actionTypeSelect.openDropdown());
    const optionLabels = actionTypeSelect
      .findDropdown()
      .findOptions()
      .map((o) => o.getElement().textContent);

    expect(optionLabels).toEqual(['Http', 'Notification']);
    expect(optionLabels.join(' ')).not.toMatch(/command/i);
    expect(optionLabels.join(' ')).not.toMatch(/isolate|block|disable|quarantine/i);
  });

  it('constrains min severity to the real backend enum values', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    const minSeveritySelect = createWrapper(dialog).findAllSelects()[0];
    act(() => minSeveritySelect.openDropdown());
    const minSeverityValues = minSeveritySelect
      .findDropdown()
      .findOptions()
      .map((o) => o.getElement().textContent);
    expect(minSeverityValues.join(' ')).toMatch(/Critical.*High.*Medium.*Low/s);

    expect(dialog).toBeInTheDocument();
  });

  it('renders a Webhook URL field for the http action type and posts {"url": ...} as action_config', async () => {
    const f = routedFetch({ list: { items: [] } });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'Notify on scan' } });
    fireEvent.change(within(dialog).getByLabelText('Webhook URL'), { target: { value: 'https://example.com/hook' } });

    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const createCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/security-response-rules') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    expect(JSON.parse(String((createCall[1] as RequestInit).body))).toEqual({
      name: 'Notify on scan',
      min_severity: 'high',
      asset_id: undefined,
      action_type: 'http',
      action_config: { url: 'https://example.com/hook' },
      enabled: true,
    });
  });

  it('switches to a notification JSON config editor when action_type is notification', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    const actionTypeSelect = createWrapper(dialog).findAllSelects()[1];
    act(() => actionTypeSelect.openDropdown());
    act(() => actionTypeSelect.selectOptionByValue('notification'));

    expect(within(dialog).queryByLabelText('Webhook URL')).not.toBeInTheDocument();
    expect(within(dialog).getByLabelText('Notification config')).toBeInTheDocument();

    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'Notify SOC' } });
    fireEvent.change(within(dialog).getByLabelText('Notification config'), { target: { value: 'not json' } });

    expect(within(dialog).getByText('Must be valid JSON.')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
  });

  it('surfaces a validation error instead of closing the dialog', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] }, create: 'ERROR' }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Create rule' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'Bad rule' } });
    fireEvent.change(within(dialog).getByLabelText('Webhook URL'), { target: { value: 'https://example.com/hook' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('action_type must be one of the SAFE actions');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});
