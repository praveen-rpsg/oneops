import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from './test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { IncidentDTO, IncidentEventDTO } from './incidents';
import type { TenantUserDTO } from './users';

// Exercises the write-action pattern E-ACT.1/ADR-ACT-001 establishes
// (optimistic-lock row_version round-trip, legal-transition gating,
// confirm-then-send for consequential transitions, 409-triggers-refetch,
// error surfacing) through the real board -> split-panel integration, the
// same way ArtifactDetail.test.tsx drives the ratify flow through the real
// estate -> artifact page rather than mounting the panel in isolation.

const incident = (over: Partial<IncidentDTO> = {}): IncidentDTO => ({
  incident_id: 'INC-1',
  title: 'API 5xx spike',
  description: 'Elevated 5xx rate on the public API.',
  severity: 'high',
  status: 'open',
  source: 'manual',
  row_version: 1,
  created_at: '2026-08-06T08:00:00Z',
  updated_at: '2026-08-06T08:00:00Z',
  is_root: true,
  ...over,
});

const rootTimeline: IncidentEventDTO[] = [
  { event_id: 'EV-1', incident_id: 'INC-1', kind: 'created', actor: 'operator', row_version: 1, occurred_at: '2026-08-06T08:00:00Z' },
];

const alice: TenantUserDTO = {
  user_id: 'U-ALICE',
  email: 'alice@example.com',
  display_name: 'Alice',
};

interface Fixture {
  list?: unknown;
  initial: IncidentDTO;
  users?: TenantUserDTO[] | 'ERROR';
  /** 'OK' (default) applies the requested change; 'CONFLICT'/'ERROR' short-circuit every transition/assign call. */
  transitionResult?: 'OK' | 'CONFLICT' | 'ERROR';
  assignResult?: 'OK' | 'CONFLICT' | 'ERROR';
}

/**
 * A tiny, stateful fake of the incident admin API: POST transition/assign
 * mutate an in-memory row so a subsequent GET (the refetch every action
 * triggers) reflects the change — exactly what proves "refetch shows the new
 * state" rather than merely "the right body was sent".
 */
function routedFetch(fx: Fixture) {
  const detail: Record<string, IncidentDTO> = { [fx.initial.incident_id]: fx.initial };
  const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
  const fail = (status: number, body: Record<string, unknown>) => ({
    ok: false,
    status,
    json: async () => ({ status, ...body }),
  });

  return vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url);
    const method = init?.method ?? 'GET';

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    if (u.includes('/admin/tenant-users')) {
      if (fx.users === 'ERROR') return fail(403, { title: 'forbidden', detail: 'missing permission: admin' });
      return ok({ items: fx.users ?? [] });
    }

    const transitionMatch = u.match(/incidents\/([^/?]+)\/transition/);
    if (method === 'POST' && transitionMatch) {
      const id = transitionMatch[1];
      if (fx.transitionResult === 'CONFLICT') {
        return fail(409, { title: 'conflict', detail: 'incident was modified concurrently; re-read and retry' });
      }
      if (fx.transitionResult === 'ERROR') {
        return fail(422, { title: 'validation failed', detail: 'status must be one of: open, acknowledged, ...' });
      }
      const body = JSON.parse(String(init?.body)) as { status: string; row_version: number };
      const current = detail[id];
      const updated: IncidentDTO = { ...current, status: body.status as IncidentDTO['status'], row_version: current.row_version + 1 };
      detail[id] = updated;
      return ok(updated);
    }

    const assignMatch = u.match(/incidents\/([^/?]+)\/assign/);
    if (method === 'POST' && assignMatch) {
      const id = assignMatch[1];
      if (fx.assignResult === 'CONFLICT') {
        return fail(409, { title: 'conflict', detail: 'incident was modified concurrently; re-read and retry' });
      }
      if (fx.assignResult === 'ERROR') {
        return fail(404, { title: 'not found', detail: 'assignee_user_id does not name a user with an active membership in this tenant' });
      }
      const body = JSON.parse(String(init?.body)) as { assignee_user_id: string | null; row_version: number };
      const current = detail[id];
      const updated: IncidentDTO = { ...current, row_version: current.row_version + 1 };
      if (body.assignee_user_id) updated.assignee_user_id = body.assignee_user_id;
      else delete updated.assignee_user_id;
      detail[id] = updated;
      return ok(updated);
    }

    const timelineMatch = u.match(/incidents\/([^/?]+)\/timeline/);
    if (timelineMatch) return ok({ items: rootTimeline });

    const detailMatch = u.match(/admin\/incidents\/([^/?]+)/);
    if (detailMatch) {
      const body = detail[detailMatch[1]];
      if (!body) return fail(404, { title: 'not found' });
      return ok(body);
    }

    if (u.includes('/admin/incidents')) return ok(fx.list ?? { items: [fx.initial] });

    return ok({});
  });
}

/** Finds the Select currently showing `name` as its trigger text — several Selects can be on screen at once. */
function selectShowing(name: string) {
  const found = createWrapper()
    .findAllSelects()
    .find((s) => s.findTrigger().getElement().textContent?.includes(name));
  return found ?? null;
}

async function openPanel() {
  fireEvent.click(await screen.findByRole('button', { name: 'API 5xx spike' }));
  await screen.findByText('Elevated 5xx rate on the public API.');
}

beforeEach(() => window.history.replaceState(null, '', '/incidents'));
afterEach(() => vi.unstubAllGlobals());

describe('incident detail actions', () => {
  it('offers only legal transitions for the current status', async () => {
    vi.stubGlobal('fetch', routedFetch({ initial: incident({ status: 'open' }) }));
    renderApp();
    await openPanel();

    expect(await screen.findByRole('button', { name: 'Acknowledge' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Start investigating' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Resolve' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Close' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reopen' })).not.toBeInTheDocument();
  });

  it('offers both legal moves from resolved (close or reopen)', async () => {
    vi.stubGlobal('fetch', routedFetch({ initial: incident({ status: 'resolved' }) }));
    renderApp();
    await openPanel();

    expect(await screen.findByRole('button', { name: 'Close' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Reopen' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Acknowledge' })).not.toBeInTheDocument();
  });

  it('offers no transition from closed — it is terminal', async () => {
    vi.stubGlobal('fetch', routedFetch({ initial: incident({ status: 'closed' }) }));
    renderApp();
    await openPanel();

    expect(await screen.findByText(/Closed is terminal/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reopen' })).not.toBeInTheDocument();
  });

  it('sends the target status and row_version on a non-consequential transition, then refetches', async () => {
    const f = routedFetch({ initial: incident({ status: 'open', row_version: 1 }) });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    const getsBefore = f.mock.calls.filter(([u]) => /admin\/incidents\/INC-1$/.test(String(u))).length;

    fireEvent.click(await screen.findByRole('button', { name: 'Acknowledge' }));

    await waitFor(() => expect(screen.getByText('Acknowledged')).toBeInTheDocument());

    const transitionCall = f.mock.calls.find(([u]) => String(u).includes('/incidents/INC-1/transition'))!;
    expect(JSON.parse(String(transitionCall[1]?.body))).toEqual({ row_version: 1, status: 'acknowledged' });

    const getsAfter = f.mock.calls.filter(([u]) => /admin\/incidents\/INC-1$/.test(String(u))).length;
    expect(getsAfter).toBeGreaterThan(getsBefore);
  });

  it('confirms before a consequential transition (resolve), and does not send until confirmed', async () => {
    const f = routedFetch({ initial: incident({ status: 'investigating', row_version: 2 }) });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    fireEvent.click(await screen.findByRole('button', { name: 'Resolve' }));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent(/marks the incident as resolved/);
    expect(f.mock.calls.some(([u]) => String(u).includes('/transition'))).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(f.mock.calls.some(([u]) => String(u).includes('/transition'))).toBe(false);

    fireEvent.click(screen.getByRole('button', { name: 'Resolve' }));
    const dialog2 = await screen.findByRole('dialog');
    fireEvent.click(within(dialog2).getByRole('button', { name: 'Resolve' }));

    await waitFor(() => expect(screen.getByText('Resolved')).toBeInTheDocument());
    const call = f.mock.calls.find(([u]) => String(u).includes('/transition'))!;
    expect(JSON.parse(String(call[1]?.body))).toEqual({ row_version: 2, status: 'resolved' });
  });

  it('on a 409 conflict: refetches and shows a notice, and never blindly retries the mutation', async () => {
    const f = routedFetch({ initial: incident({ status: 'open', row_version: 1 }), transitionResult: 'CONFLICT' });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    fireEvent.click(await screen.findByRole('button', { name: 'Acknowledge' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/Changed since you loaded it/);

    // The incident is refetched (still 'open' — the fixture never actually applies
    // the transition on CONFLICT) but the mutation itself is sent exactly once —
    // a real retry-storm would show more than one POST to /transition.
    await waitFor(() =>
      expect(f.mock.calls.filter(([u]) => /admin\/incidents\/INC-1$/.test(String(u))).length).toBeGreaterThan(1),
    );
    expect(f.mock.calls.filter(([u]) => String(u).includes('/transition'))).toHaveLength(1);
  });

  it('assigns the picked user with row_version, and refetches to show it', async () => {
    const f = routedFetch({ initial: incident({ status: 'open', row_version: 1 }), users: [alice] });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    // Both the key-value summary and the picker's own trigger read "Unassigned" to start.
    expect((await screen.findAllByText('Unassigned')).length).toBeGreaterThanOrEqual(2);

    const assigneeSelect = await waitFor(() => {
      const s = selectShowing('Unassigned');
      if (!s) throw new Error('assignee select not ready');
      return s;
    });
    act(() => assigneeSelect.openDropdown());
    act(() => assigneeSelect.selectOptionByValue('U-ALICE'));

    fireEvent.click(screen.getByRole('button', { name: 'Assign' }));

    await waitFor(() => expect(screen.getByText('U-ALICE')).toBeInTheDocument());

    const call = f.mock.calls.find(([u]) => String(u).includes('/assign'))!;
    expect(JSON.parse(String(call[1]?.body))).toEqual({ row_version: 1, assignee_user_id: 'U-ALICE' });
  });

  it('falls back to a free-text user id input when the user directory is unavailable, and still assigns', async () => {
    const f = routedFetch({ initial: incident({ status: 'open', row_version: 1 }), users: 'ERROR' });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    const input = await screen.findByPlaceholderText(/user id/i);
    fireEvent.change(input, { target: { value: 'U-BOB' } });
    fireEvent.click(screen.getByRole('button', { name: 'Assign' }));

    await waitFor(() => expect(screen.getByText('U-BOB')).toBeInTheDocument());
    const call = f.mock.calls.find(([u]) => String(u).includes('/assign'))!;
    expect(JSON.parse(String(call[1]?.body))).toEqual({ row_version: 1, assignee_user_id: 'U-BOB' });
  });

  it('surfaces a mutation error inline rather than failing silently', async () => {
    vi.stubGlobal(
      'fetch',
      routedFetch({ initial: incident({ status: 'open', row_version: 1 }), users: 'ERROR', assignResult: 'ERROR' }),
    );
    renderApp();
    await openPanel();

    const input = await screen.findByPlaceholderText(/user id/i);
    fireEvent.change(input, { target: { value: 'U-GHOST' } });
    fireEvent.click(screen.getByRole('button', { name: 'Assign' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/does not name a user with an active membership/);
    // The stale assignment is left alone — no silent overwrite to a bad state.
    expect(screen.getByPlaceholderText(/user id/i)).toHaveValue('U-GHOST');
  });
});
