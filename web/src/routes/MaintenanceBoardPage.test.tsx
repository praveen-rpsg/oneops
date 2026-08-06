import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { MaintenanceWindowDTO } from '../maintenanceWindows';

// Exercises E-ACT.3/ADR-ACT-004's write actions (Schedule maintenance,
// Cancel) against a fixed "now" so active/upcoming/expired status is
// deterministic — the DTO itself carries no status field, it is computed
// client-side from `now` against each window's own half-open
// [starts_at, ends_at) bounds.

const NOW = new Date('2026-08-06T12:00:00.000Z');

const win = (over: Partial<MaintenanceWindowDTO> = {}): MaintenanceWindowDTO => ({
  window_id: 'MW-1',
  asset_id: 'AST-WEB-1',
  starts_at: '2026-08-06T11:00:00Z',
  ends_at: '2026-08-06T13:00:00Z',
  reason: 'patching window',
  created_by: 'user-1',
  suppressed_count: 0,
  row_version: 1,
  created_at: '2026-08-06T08:00:00Z',
  updated_at: '2026-08-06T08:00:00Z',
  ...over,
});

const upcoming = win({
  window_id: 'MW-2',
  asset_id: 'AST-DB-1',
  starts_at: '2026-08-07T00:00:00Z',
  ends_at: '2026-08-07T02:00:00Z',
  reason: 'network maintenance',
});

const expired = win({
  window_id: 'MW-3',
  asset_id: 'AST-CACHE-1',
  starts_at: '2026-08-05T00:00:00Z',
  ends_at: '2026-08-05T02:00:00Z',
  reason: '',
  suppressed_count: 4,
});

interface Fixture {
  list?: unknown;
  /** 'ERROR' makes POST /v1/admin/maintenance-windows fail 422. */
  create?: 'ERROR';
  /** 'ERROR' makes DELETE /v1/admin/maintenance-windows/{id} fail 404. */
  cancel?: 'ERROR';
}

function routedFetch(fx: Fixture = {}) {
  const items = [...((fx.list as { items: MaintenanceWindowDTO[] } | undefined)?.items ?? [win(), upcoming, expired])];
  return vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url);
    const method = init?.method ?? 'GET';
    const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
    const fail = (status: number, body: Record<string, unknown>) => ({
      ok: false,
      status,
      json: async () => ({ status, ...body }),
    });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    if (method === 'POST' && /\/admin\/maintenance-windows$/.test(u)) {
      if (fx.create === 'ERROR') {
        return fail(422, { title: 'validation failed', detail: 'ends_at must be strictly after starts_at' });
      }
      const body = JSON.parse(String(init?.body)) as Partial<MaintenanceWindowDTO>;
      const created = win({
        window_id: 'MW-NEW',
        asset_id: body.asset_id ?? '',
        starts_at: body.starts_at ?? '',
        ends_at: body.ends_at ?? '',
        reason: body.reason ?? '',
      });
      items.push(created);
      return ok(created, 201);
    }

    const idMatch = u.match(/maintenance-windows\/([^/?]+)/);
    if (method === 'DELETE' && idMatch) {
      if (fx.cancel === 'ERROR') {
        return fail(404, { title: 'not found', detail: 'no such maintenance window' });
      }
      const i = items.findIndex((w) => w.window_id === idMatch[1]);
      if (i >= 0) items.splice(i, 1);
      return { ok: true, status: 204, json: async () => undefined };
    }

    if (u.includes('/admin/maintenance-windows')) return ok({ items });

    return ok({});
  });
}

beforeEach(() => {
  window.history.replaceState(null, '', '/maintenance');
  // Only `Date` is faked — testing-library's `waitFor`/`findBy*` rely on real
  // timers to poll, so faking setTimeout/setInterval too would hang them.
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(NOW);
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('maintenance board', () => {
  it('renders every window from a mocked list with active/upcoming/expired computed', async () => {
    vi.stubGlobal('fetch', routedFetch());
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Maintenance/ })).toBeInTheDocument();
    expect(await screen.findByText('AST-WEB-1')).toBeInTheDocument();
    expect(screen.getByText('AST-DB-1')).toBeInTheDocument();
    expect(screen.getByText('AST-CACHE-1')).toBeInTheDocument();
    expect(screen.getByText('Active now')).toBeInTheDocument();
    expect(screen.getByText('Upcoming')).toBeInTheDocument();
    expect(screen.getByText('Expired')).toBeInTheDocument();
    expect(screen.getByText('3 of 3 windows')).toBeInTheDocument();
  });

  it('shows an explicit empty state with no windows', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] } }));
    renderApp();

    expect(await screen.findByText('No maintenance windows')).toBeInTheDocument();
    expect(screen.getByText(/No maintenance windows have been declared/)).toBeInTheDocument();
  });

  it('shows an error banner on failure and lets the operator retry', async () => {
    const fetchMock = vi.fn().mockImplementation(async (url: string) => {
      const u = String(url);
      if (u.includes('/auth/config')) return { ok: true, status: 200, json: async () => ({ auth_enabled: false }) };
      return {
        ok: false,
        status: 500,
        json: async () => ({ title: 'internal error', status: 500, detail: 'boom' }),
      };
    });
    vi.stubGlobal('fetch', fetchMock);
    renderApp();

    expect(await screen.findByRole('alert')).toHaveTextContent('internal error');

    const callsBefore = fetchMock.mock.calls.length;
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));

    await vi.waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(callsBefore));
  });
});

describe('schedule maintenance', () => {
  it('is disabled until asset id and a valid start/end are filled', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [win()] } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Schedule maintenance' }));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).getByRole('button', { name: 'Schedule' })).toBeDisabled();
  });

  it('posts the exact create body (RFC3339 UTC) and refetches the board', async () => {
    const f = routedFetch({ list: { items: [win()] } });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Schedule maintenance' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Asset ID'), { target: { value: 'AST-NEW' } });
    fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'planned patching' } });

    const dateInputs = createWrapper(dialog).findAllDateInputs();
    const timeInputs = createWrapper(dialog).findAllTimeInputs();
    act(() => dateInputs[0].setInputValue('2026/08/10'));
    act(() => timeInputs[0].setInputValue('09:00'));
    act(() => dateInputs[1].setInputValue('2026/08/10'));
    act(() => timeInputs[1].setInputValue('11:00'));

    await waitFor(() => expect(within(dialog).getByRole('button', { name: 'Schedule' })).not.toBeDisabled());

    const listCallsBefore = f.mock.calls.filter(([u]) => String(u).includes('/admin/maintenance-windows?')).length;

    fireEvent.click(within(dialog).getByRole('button', { name: 'Schedule' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const createCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/maintenance-windows') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    const body = JSON.parse(String((createCall[1] as RequestInit).body)) as Record<string, unknown>;
    expect(body.asset_id).toBe('AST-NEW');
    expect(body.reason).toBe('planned patching');
    expect(body.starts_at).toBe(new Date('2026-08-10T09:00:00').toISOString());
    expect(body.ends_at).toBe(new Date('2026-08-10T11:00:00').toISOString());
    expect(new Date(body.ends_at as string).getTime()).toBeGreaterThan(new Date(body.starts_at as string).getTime());

    await waitFor(() =>
      expect(f.mock.calls.filter(([u]) => String(u).includes('/admin/maintenance-windows?')).length).toBeGreaterThan(
        listCallsBefore,
      ),
    );
  });

  it('blocks submission client-side when end is not strictly after start (half-open [start, end))', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [win()] } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Schedule maintenance' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Asset ID'), { target: { value: 'AST-NEW' } });

    const dateInputs = createWrapper(dialog).findAllDateInputs();
    const timeInputs = createWrapper(dialog).findAllTimeInputs();
    act(() => dateInputs[0].setInputValue('2026/08/10'));
    act(() => timeInputs[0].setInputValue('09:00'));
    // End equal to start — not strictly after, so the half-open rule rejects it.
    act(() => dateInputs[1].setInputValue('2026/08/10'));
    act(() => timeInputs[1].setInputValue('09:00'));

    await waitFor(() => expect(within(dialog).getByText(/must be strictly after start/)).toBeInTheDocument());
    expect(within(dialog).getByRole('button', { name: 'Schedule' })).toBeDisabled();

    // An earlier end is rejected the same way.
    act(() => timeInputs[1].setInputValue('08:00'));
    expect(within(dialog).getByRole('button', { name: 'Schedule' })).toBeDisabled();
  });

  it('surfaces a validation error instead of closing the dialog', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [win()] }, create: 'ERROR' }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Schedule maintenance' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Asset ID'), { target: { value: 'AST-BAD' } });
    const dateInputs = createWrapper(dialog).findAllDateInputs();
    const timeInputs = createWrapper(dialog).findAllTimeInputs();
    act(() => dateInputs[0].setInputValue('2026/08/10'));
    act(() => timeInputs[0].setInputValue('09:00'));
    act(() => dateInputs[1].setInputValue('2026/08/10'));
    act(() => timeInputs[1].setInputValue('11:00'));

    await waitFor(() => expect(within(dialog).getByRole('button', { name: 'Schedule' })).not.toBeDisabled());
    fireEvent.click(within(dialog).getByRole('button', { name: 'Schedule' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('ends_at must be strictly after starts_at');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});

describe('cancel maintenance window', () => {
  it('confirms then deletes with no row_version (the confirmed contract), then refetches the board', async () => {
    const f = routedFetch({ list: { items: [win()] } });
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByText('AST-WEB-1');
    const listCallsBefore = f.mock.calls.filter(([u]) => String(u).includes('/admin/maintenance-windows?')).length;

    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent(/permanently withdraws the window/);
    expect(f.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel window' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const deleteCall = f.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')!;
    expect(deleteCall[1]).not.toHaveProperty('body');

    await waitFor(() =>
      expect(f.mock.calls.filter(([u]) => String(u).includes('/admin/maintenance-windows?')).length).toBeGreaterThan(
        listCallsBefore,
      ),
    );
  });

  it('surfaces a cancel error inline rather than failing silently', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [win()] }, cancel: 'ERROR' }));
    renderApp();

    await screen.findByText('AST-WEB-1');
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel window' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('no such maintenance window');
    // The window is still here — no silent removal on a failed cancel.
    expect(screen.getByText('AST-WEB-1')).toBeInTheDocument();
  });
});
