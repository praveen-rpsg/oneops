import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from './test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { setToken } from './auth';
import type { ConfigObject } from './api';

const artifact = (over: Partial<ConfigObject> = {}): ConfigObject => ({
  cfg_id: 'ROOT',
  artifact: 'Draft-Policy.md',
  version: '1.0.0',
  role: 'governance',
  lifecycle: 'draft',
  retention_class: 'working_material',
  authority: 'non_normative',
  row_version: 1,
  created_at: '2026-07-24T12:00:00Z',
  updated_at: '2026-07-24T12:00:00Z',
  ...over,
});

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });
const fail = (status: number) => ({
  ok: false,
  status,
  json: async () => ({ title: 'error', status }),
});

/** History is chronological (server-fixed order); the console reverses it. */
const HISTORY_TWO = {
  items: [
    {
      seq: 1,
      operation: 'approval',
      actor: 'marcus@partner.example',
      occurred_at: '2026-07-20T09:00:00Z',
      operation_id: 'op-1',
      resulting_state: {
        lifecycle: 'approved',
        retention_class: 'working_material',
        authority: 'non_normative',
      },
    },
    {
      seq: 2,
      operation: 'ratification',
      actor: 'priya@partner.example',
      occurred_at: '2026-07-24T10:00:00Z',
      operation_id: 'op-2',
      resulting_state: {
        lifecycle: 'ratified',
        retention_class: 'current_baseline',
        authority: 'active',
      },
    },
  ],
};

const EVENTS_TWO = {
  order: 'asc',
  items: [
    { seq: 1, event_id: 'ev-1', operation_id: 'op-1', operation: 'approval', actor: 'm', occurred_at: '2026-07-20T09:00:00Z', this_hash: 'aaa111' },
    { seq: 2, event_id: 'ev-2', operation_id: 'op-2', operation: 'ratification', actor: 'p', occurred_at: '2026-07-24T10:00:00Z', prev_hash: 'aaa111', this_hash: 'bbb222' },
  ],
};

function mock(opts: { history?: unknown; events?: unknown; chain?: unknown } = {}) {
  return vi.fn().mockImplementation(async (url: string) => {
    const u = String(url);
    if (u.includes('/auth/config')) return ok({ auth_enabled: false });
    if (u.includes('/history')) return (opts.history as never) ?? ok(HISTORY_TWO);
    if (u.includes('/audit/events')) return (opts.events as never) ?? ok(EVENTS_TWO);
    if (u.includes('/audit')) {
      return (opts.chain as never) ?? ok({ chain_id: 'ROOT', verified: true, checked: 2, head_seq: 2, head_hash: 'bbb222' });
    }
    if (u.includes('/dependencies') || u.includes('/dependents')) {
      return ok({ root: 'ROOT', direction: 'dependencies', count: 0, nodes: [] });
    }
    if (u.includes('/v1/artifacts/ROOT')) return ok(artifact());
    return ok({ items: [artifact()] });
  });
}

const timeline = async () =>
  (await screen.findByRole('heading', { name: /Audit record/ })).closest('section')!;

beforeEach(() => {
  window.history.replaceState(null, '', '/artifacts/ROOT');
  setToken(null);
});
afterEach(() => vi.unstubAllGlobals());

describe('audit timeline', () => {
  it('shows who did what, newest first', async () => {
    vi.stubGlobal('fetch', mock());
    renderApp();
    const panel = await timeline();

    const items = await within(panel).findAllByRole('listitem');
    expect(within(items[0]).getByText('Ratification')).toBeInTheDocument();
    expect(within(items[0]).getByText('priya@partner.example')).toBeInTheDocument();
    expect(within(items[1]).getByText('Approval')).toBeInTheDocument();
  });

  it('derives before → after from consecutive events', async () => {
    vi.stubGlobal('fetch', mock());
    renderApp();
    const panel = await timeline();
    const newest = (await within(panel).findAllByRole('listitem'))[0];

    // approved → ratified, non_normative → active, working_material → current_baseline
    expect(within(newest).getByText('Approved')).toBeInTheDocument();
    expect(within(newest).getByText('Ratified')).toBeInTheDocument();
    expect(within(newest).getByText('Non normative')).toBeInTheDocument();
    expect(within(newest).getByText('Active')).toBeInTheDocument();
  });

  it('shows the chain-verified badge from the chain summary', async () => {
    vi.stubGlobal('fetch', mock());
    renderApp();
    expect(await screen.findByText('✓ Chain verified')).toBeInTheDocument();
  });

  it('warns when the chain is not verified', async () => {
    vi.stubGlobal(
      'fetch',
      mock({ chain: ok({ chain_id: 'ROOT', verified: false, checked: 2, head_seq: 2, break_reason: 'hash mismatch at seq 2' }) }),
    );
    renderApp();
    expect(await screen.findByText('⚠ Chain unverified')).toBeInTheDocument();
  });

  it('reveals the hash record on demand, not by default', async () => {
    vi.stubGlobal('fetch', mock());
    renderApp();
    const panel = await timeline();

    expect(within(panel).queryByText('bbb222')).not.toBeInTheDocument();
    // The "Show chain record" buttons render only once the entries themselves
    // have painted — a second, later-resolving fetch than the one `timeline()`
    // awaited (the heading is static; the entry list is not) — so this must
    // poll rather than assume the click target already exists.
    fireEvent.click((await within(panel).findAllByRole('button', { name: /Show chain record/ }))[0]);

    expect(await within(panel).findByText('bbb222')).toBeInTheDocument();
    expect(within(panel).getByText('aaa111')).toBeInTheDocument(); // prev_hash links the chain
  });

  it('states plainly when nothing has happened yet', async () => {
    vi.stubGlobal('fetch', mock({ history: ok({ items: [] }) }));
    renderApp();
    const panel = await timeline();
    // The empty state only replaces "Loading…" once the (empty) history fetch
    // settles — a later tick than the static heading `timeline()` awaited.
    expect(
      await within(panel).findByText(/No governance operation has been performed/),
    ).toBeInTheDocument();
  });

  it('still shows the governance record when hashes are unavailable', async () => {
    vi.stubGlobal('fetch', mock({ events: fail(500) }));
    renderApp();
    const panel = await timeline();

    expect(await within(panel).findByText('Ratification')).toBeInTheDocument();
    expect(within(panel).queryByRole('alert')).not.toBeInTheDocument();
  });

  it('surfaces a failure of the record itself, and retries', async () => {
    const f = mock({ history: fail(500) });
    vi.stubGlobal('fetch', f);
    renderApp();
    const panel = await timeline();

    expect(await within(panel).findByRole('alert')).toBeInTheDocument();
    fireEvent.click(within(panel).getByRole('button', { name: 'Try again' }));

    await waitFor(() =>
      expect(f.mock.calls.map(String).filter((u) => u.includes('/history')).length).toBeGreaterThan(1),
    );
  });

  it('offers older pages only when the server returns a cursor', async () => {
    vi.stubGlobal('fetch', mock({ history: ok({ ...HISTORY_TWO, next_cursor: '1' }) }));
    renderApp();
    const panel = await timeline();
    expect(await within(panel).findByRole('button', { name: 'Load older' })).toBeInTheDocument();
  });

  it('refreshes after a ratification so the new event is visible', async () => {
    const f = mock();
    vi.stubGlobal('fetch', f);
    renderApp();
    await timeline();

    const before = f.mock.calls.map(String).filter((u) => u.includes('/history')).length;

    fireEvent.click(await screen.findByRole('button', { name: 'Ratify' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Ratify' }));

    await waitFor(() =>
      expect(f.mock.calls.map(String).filter((u) => u.includes('/history')).length).toBeGreaterThan(before),
    );
  });
});
