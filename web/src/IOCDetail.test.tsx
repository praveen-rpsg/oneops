import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from './test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { IOCDTO } from './iocs';

// Exercises E-SEC-UI.2's write actions (Edit, Enable/Disable, Delete) through
// the real board -> split-panel integration, the same way
// AlertRuleDetail.test.tsx/SecurityRuleDetail.test.tsx drive their own
// actions — row_version round-trip on Edit/Enable-Disable, 409-triggers-
// refetch, the confirmed no-row_version-on-delete contract, and error
// surfacing.

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

interface Fixture {
  list?: unknown;
  initial: IOCDTO;
  patchResult?: 'OK' | 'CONFLICT' | 'ERROR';
  deleteResult?: 'OK' | 'ERROR';
}

/**
 * A tiny, stateful fake of the ioc admin API: PATCH mutates an in-memory row
 * so a subsequent GET (the refetch every action triggers) reflects the
 * change; DELETE clears it.
 */
function routedFetch(fx: Fixture) {
  const detail: Record<string, IOCDTO> = { [fx.initial.ioc_id]: fx.initial };
  const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
  const fail = (status: number, body: Record<string, unknown>) => ({ ok: false, status, json: async () => ({ status, ...body }) });

  return vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url);
    const method = init?.method ?? 'GET';

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    const idMatch = u.match(/admin\/iocs\/([^/?]+)/);

    if (method === 'PATCH' && idMatch) {
      const id = idMatch[1];
      if (fx.patchResult === 'CONFLICT') {
        return fail(409, { title: 'conflict', detail: 'ioc was modified concurrently; re-read and retry' });
      }
      if (fx.patchResult === 'ERROR') {
        return fail(422, { title: 'validation failed', detail: 'description must be at most 2000 characters' });
      }
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      const current = detail[id];
      const updated: IOCDTO = {
        ...current,
        ...(body.severity !== undefined ? { severity: body.severity as IOCDTO['severity'] } : {}),
        ...(body.enabled !== undefined ? { enabled: body.enabled as boolean } : {}),
        ...(body.description !== undefined ? { description: body.description as string } : {}),
        ...(body.source !== undefined ? { source: body.source as string } : {}),
        row_version: current.row_version + 1,
      };
      detail[id] = updated;
      return ok(updated);
    }

    if (method === 'DELETE' && idMatch) {
      const id = idMatch[1];
      if (fx.deleteResult === 'ERROR') return fail(404, { title: 'not found', detail: 'no such ioc' });
      delete detail[id];
      return { ok: true, status: 204, json: async () => undefined };
    }

    if (idMatch) {
      const body = detail[idMatch[1]];
      if (!body) return fail(404, { title: 'not found' });
      return ok(body);
    }

    if (u.includes('/admin/iocs')) return ok(fx.list ?? { items: [fx.initial] });

    return ok({});
  });
}

async function openPanel() {
  fireEvent.click(await screen.findByRole('button', { name: '198.51.100.23' }));
  await screen.findByRole('button', { name: 'Edit' });
}

beforeEach(() => window.history.replaceState(null, '', '/security/indicators'));
afterEach(() => vi.unstubAllGlobals());

describe('ioc detail actions', () => {
  it('edits the indicator: sends row_version + the patched fields, then refetches to show the new values', async () => {
    const f = routedFetch({ initial: ioc({ row_version: 1 }) });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog');

    const severitySelect = createWrapper(dialog).findAllSelects()[0];
    act(() => severitySelect.openDropdown());
    act(() => severitySelect.selectOptionByValue('critical'));

    fireEvent.change(within(dialog).getByLabelText('Source'), { target: { value: 'alienvault-otx' } });

    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    await waitFor(() => expect(screen.getAllByText('alienvault-otx').length).toBeGreaterThan(0));

    const patchCall = f.mock.calls.find(
      ([u, init]) => String(u).includes('/admin/iocs/IOC-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    expect(JSON.parse(String((patchCall[1] as RequestInit).body))).toEqual({
      row_version: 1,
      severity: 'critical',
      enabled: true,
      description: 'Known C2 relay.',
      source: 'alienvault-otx',
    });
  });

  it('does not offer indicator_type/indicator_value as editable — they are fixed at creation', async () => {
    vi.stubGlobal('fetch', routedFetch({ initial: ioc() }));
    renderApp();
    await openPanel();

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).queryByLabelText('Indicator value')).not.toBeInTheDocument();
    expect(dialog).toHaveTextContent(/Ip.*198\.51\.100\.23.*not editable/);
  });

  it('on a 409 conflict while editing: closes the dialog, refetches, and shows a notice without retrying', async () => {
    const f = routedFetch({ initial: ioc({ row_version: 1 }), patchResult: 'CONFLICT' });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/Changed since you loaded it/);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();

    expect(
      f.mock.calls.filter(([u, init]) => String(u).includes('/admin/iocs/IOC-1') && (init as RequestInit | undefined)?.method === 'PATCH'),
    ).toHaveLength(1);
    await waitFor(() =>
      expect(
        f.mock.calls.filter(([u, init]) => /admin\/iocs\/IOC-1$/.test(String(u)) && (init as RequestInit | undefined)?.method === undefined).length,
      ).toBeGreaterThan(1),
    );
  });

  it('enables/disables with row_version, then refetches', async () => {
    const f = routedFetch({ initial: ioc({ enabled: true, row_version: 1 }) });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    fireEvent.click(screen.getByRole('button', { name: 'Disable' }));

    await waitFor(() => expect(screen.getByRole('button', { name: 'Enable' })).toBeInTheDocument());

    const patchCall = f.mock.calls.find(
      ([u, init]) => String(u).includes('/admin/iocs/IOC-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    expect(JSON.parse(String((patchCall[1] as RequestInit).body))).toEqual({ row_version: 1, enabled: false });
  });

  it('deletes with a confirmation modal first, sends no row_version (the confirmed contract), then closes the panel and refetches the board', async () => {
    const f = routedFetch({ initial: ioc() });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    const listCallsBefore = f.mock.calls.filter(([u]) => String(u).includes('/admin/iocs?')).length;

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent(/permanently removes the watchlist entry/);
    expect(f.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument());

    const deleteCall = f.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')!;
    expect(deleteCall[1]).not.toHaveProperty('body');

    await waitFor(() =>
      expect(f.mock.calls.filter(([u]) => String(u).includes('/admin/iocs?')).length).toBeGreaterThan(listCallsBefore),
    );
  });

  it('surfaces a delete error inline rather than failing silently', async () => {
    vi.stubGlobal('fetch', routedFetch({ initial: ioc(), deleteResult: 'ERROR' }));
    renderApp();
    await openPanel();

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('no such ioc');
    expect(screen.getByRole('button', { name: 'Edit' })).toBeInTheDocument();
  });
});
