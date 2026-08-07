import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { IOCDTO } from '../iocs';

// Exercises E-SEC-UI.2's Indicators screen: list + filter (GET
// /v1/admin/iocs), "Add indicator" (POST .../iocs) with every enum field a
// Select constrained to the real backend value set, a duplicate
// (tenant, indicator_type, indicator_value) surfaced inline as 409, the
// explicit empty state, error/retry, and the 403-graceful permission state.

const ioc = (over: Partial<IOCDTO> = {}): IOCDTO => ({
  ioc_id: 'IOC-1',
  indicator_type: 'ip',
  indicator_value: '198.51.100.23',
  severity: 'high',
  enabled: true,
  description: 'Known C2 relay.',
  source: 'misp',
  row_version: 1,
  created_at: '2026-08-06T08:00:00Z',
  updated_at: '2026-08-06T08:00:00Z',
  ...over,
});

const domainIoc = ioc({
  ioc_id: 'IOC-2',
  indicator_type: 'domain',
  indicator_value: 'evil.example.com',
  severity: 'critical',
  description: '',
  source: '',
});

interface Fixture {
  list?: unknown;
  detail?: Record<string, IOCDTO>;
  /** 'CONFLICT' makes POST /v1/admin/iocs fail 409, as a duplicate would. 'ERROR' fails it 422. */
  create?: 'CONFLICT' | 'ERROR';
  /** GET /admin/iocs answers 403 — not a tenant admin. */
  forbidden?: boolean;
}

function routedFetch(fx: Fixture = {}) {
  const created: Record<string, IOCDTO> = {};
  return vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url);
    const method = init?.method ?? 'GET';
    const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
    const fail = (status: number, body: Record<string, unknown>) => ({ ok: false, status, json: async () => ({ status, ...body }) });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    if (method === 'POST' && /\/admin\/iocs$/.test(u)) {
      if (fx.create === 'CONFLICT') {
        return fail(409, { title: 'conflict', detail: 'this tenant already has an ioc with the same indicator_type and indicator_value' });
      }
      if (fx.create === 'ERROR') {
        return fail(422, { title: 'validation failed', detail: 'indicator_value must be at most 2048 characters' });
      }
      const body = JSON.parse(String(init?.body)) as Partial<IOCDTO>;
      const newIoc = ioc({
        ioc_id: 'IOC-NEW',
        indicator_type: body.indicator_type ?? 'ip',
        indicator_value: body.indicator_value ?? '',
        severity: body.severity ?? 'low',
        enabled: body.enabled ?? true,
        description: body.description ?? '',
        source: body.source ?? '',
      });
      created[newIoc.ioc_id] = newIoc;
      return ok(newIoc, 201);
    }

    const detailMatch = u.match(/admin\/iocs\/([^/?]+)/);
    if (detailMatch) {
      const id = detailMatch[1];
      const body = fx.detail?.[id] ?? created[id];
      if (!body) return fail(404, { title: 'not found' });
      return ok(body);
    }

    if (u.includes('/admin/iocs')) {
      if (fx.forbidden) return fail(403, { title: 'forbidden', detail: 'missing permission: admin' });
      return ok(fx.list ?? { items: [] });
    }

    return ok({});
  });
}

beforeEach(() => window.history.replaceState(null, '', '/security/indicators'));
afterEach(() => vi.unstubAllGlobals());

describe('indicators board', () => {
  it('renders every indicator from a mocked list', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [ioc(), domainIoc] } }));
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Indicators/ })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: '198.51.100.23' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'evil.example.com' })).toBeInTheDocument();
    expect(screen.getByText('2 of 2 indicators')).toBeInTheDocument();
  });

  it('filters by type', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [ioc(), domainIoc] } }));
    const { container } = renderApp();

    await screen.findByRole('button', { name: '198.51.100.23' });

    const typeSelect = createWrapper(container).findAllSelects()[0];
    act(() => typeSelect.openDropdown());
    act(() => typeSelect.selectOptionByValue('domain'));

    await waitFor(() => expect(screen.queryByRole('button', { name: '198.51.100.23' })).not.toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'evil.example.com' })).toBeInTheDocument();
  });

  it('filters by severity', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [ioc(), domainIoc] } }));
    const { container } = renderApp();

    await screen.findByRole('button', { name: '198.51.100.23' });

    const severitySelect = createWrapper(container).findAllSelects()[1];
    act(() => severitySelect.openDropdown());
    act(() => severitySelect.selectOptionByValue('critical'));

    await waitFor(() => expect(screen.queryByRole('button', { name: '198.51.100.23' })).not.toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'evil.example.com' })).toBeInTheDocument();
  });

  it('opens an indicator detail in the split panel on click', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [ioc()] }, detail: { 'IOC-1': ioc() } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: '198.51.100.23' }));

    await waitFor(() => expect(screen.getByText('Known C2 relay.')).toBeInTheDocument());
    expect(screen.getAllByText('misp').length).toBeGreaterThan(0);
  });

  it('shows an explicit empty state with no indicators', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] } }));
    renderApp();

    expect(await screen.findByText('No indicators')).toBeInTheDocument();
    expect(screen.getByText(/No indicators of compromise have been added/)).toBeInTheDocument();
  });

  it('renders the clean permission-needed state on a 403, not a crash', async () => {
    vi.stubGlobal('fetch', routedFetch({ forbidden: true }));
    renderApp();

    expect(
      await screen.findByText('You need tenant-admin permission to manage indicators of compromise.'),
    ).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });
});

describe('add indicator', () => {
  it('is disabled until the required fields are filled', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [ioc()] } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Add indicator' }));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).getByRole('button', { name: 'Add' })).toBeDisabled();
  });

  it('constrains indicator type/severity to the real backend enum values', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [ioc()] } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Add indicator' }));
    const dialog = await screen.findByRole('dialog');

    const typeSelect = createWrapper(dialog).findAllSelects()[0];
    act(() => typeSelect.openDropdown());
    const typeValues = typeSelect
      .findDropdown()
      .findOptions()
      .map((o) => o.getElement().textContent);
    expect(typeValues.join(' ')).toMatch(/Ip.*Domain.*Url.*File hash.*Email/s);
    act(() => typeSelect.selectOptionByValue('domain'));

    expect(dialog).toBeInTheDocument();
  });

  it('posts the exact create body and opens the new indicator', async () => {
    const f = routedFetch({ list: { items: [ioc()] } });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Add indicator' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Indicator value'), { target: { value: '203.0.113.9' } });

    const severitySelect = createWrapper(dialog).findAllSelects()[1];
    act(() => severitySelect.openDropdown());
    act(() => severitySelect.selectOptionByValue('critical'));

    fireEvent.change(within(dialog).getByLabelText('Source'), { target: { value: 'alienvault-otx' } });

    const listCallsBefore = f.mock.calls.filter(([u]) => String(u).includes('/admin/iocs?')).length;

    fireEvent.click(within(dialog).getByRole('button', { name: 'Add' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const createCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/iocs') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    expect(JSON.parse(String((createCall[1] as RequestInit).body))).toEqual({
      indicator_type: 'ip',
      indicator_value: '203.0.113.9',
      severity: 'critical',
      enabled: true,
      description: undefined,
      source: 'alienvault-otx',
    });

    await waitFor(() => expect(f.mock.calls.filter(([u]) => String(u).includes('/admin/iocs?')).length).toBeGreaterThan(listCallsBefore));
    expect(await screen.findByRole('heading', { name: '203.0.113.9' })).toBeInTheDocument();
  });

  it('surfaces a 409 duplicate inline rather than a crash, and keeps the dialog open', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [ioc()] }, create: 'CONFLICT' }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Add indicator' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Indicator value'), { target: { value: '198.51.100.23' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('already has an ioc with the same');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });

  it('surfaces a validation error instead of closing the dialog', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [ioc()] }, create: 'ERROR' }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Add indicator' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Indicator value'), { target: { value: 'x'.repeat(10) } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('indicator_value must be at most');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});
