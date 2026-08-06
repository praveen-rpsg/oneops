import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import { combineDateAndTime } from '../maintenanceWindows';
import type { OnCallNowDTO, OnCallParticipantDTO, OnCallScheduleDTO } from '../onCall';
import type { TenantUserDTO } from '../users';

const schedule = (over: Partial<OnCallScheduleDTO> = {}): OnCallScheduleDTO => ({
  schedule_id: 'SCH-PRIMARY',
  name: 'Primary on-call',
  handoff_interval_seconds: 43_200,
  rotation_start_at: '2026-08-01T00:00:00Z',
  status: 'active',
  row_version: 1,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  ...over,
});

const onCallNow: OnCallNowDTO = {
  schedule_id: 'SCH-PRIMARY',
  at: '2026-08-06T10:00:00Z',
  user_id: 'U-PRIYA',
  display_name: 'Priya Nair',
};

const participants: OnCallParticipantDTO[] = [
  {
    participant_id: 'PART-1',
    schedule_id: 'SCH-PRIMARY',
    user_id: 'U-PRIYA',
    position: 0,
    row_version: 1,
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T00:00:00Z',
  },
  {
    participant_id: 'PART-2',
    schedule_id: 'SCH-PRIMARY',
    user_id: 'U-SECOND',
    position: 1,
    row_version: 1,
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T00:00:00Z',
  },
];

interface Fixture {
  list?: unknown;
  onCall?: unknown;
  participants?: unknown;
}

function routedFetch(fx: Fixture = {}) {
  return vi.fn().mockImplementation(async (url: string) => {
    const u = String(url);
    const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    // Order matters: "/on-call-schedules/{id}/on-call" and ".../participants"
    // both contain the substring "on-call-schedules", so the more specific
    // suffix checks must run before the generic list check.
    if (/\/on-call-schedules\/[^/?]+\/on-call(\?|$)/.test(u)) {
      if (fx.onCall === 'ERROR') return { ok: false, status: 500, json: async () => ({ title: 'boom', status: 500 }) };
      return ok(fx.onCall ?? onCallNow);
    }
    if (u.includes('/participants')) {
      if (fx.participants === 'ERROR') return { ok: false, status: 500, json: async () => ({ title: 'boom', status: 500 }) };
      return ok(fx.participants ?? { items: participants });
    }
    if (u.includes('/on-call-schedules')) {
      if (fx.list === 'ERROR') {
        return { ok: false, status: 500, json: async () => ({ title: 'internal error', status: 500, detail: 'boom' }) };
      }
      return ok(fx.list ?? { items: [] });
    }

    return ok({});
  });
}

beforeEach(() => window.history.replaceState(null, '', '/on-call'));
afterEach(() => vi.unstubAllGlobals());

describe('on-call board', () => {
  it('renders the current on-call and the ordered participant roster', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [schedule()] } }));
    renderApp();

    expect(await screen.findByRole('heading', { name: /^On-call/ })).toBeInTheDocument();
    expect(await screen.findByText('Primary on-call')).toBeInTheDocument();
    expect(await screen.findByText('Priya Nair')).toBeInTheDocument();
    expect(screen.getByText('12h')).toBeInTheDocument();
    expect(screen.getByText('U-PRIYA')).toBeInTheDocument();
    expect(screen.getByText('U-SECOND')).toBeInTheDocument();
    expect(screen.getByText('on call now')).toBeInTheDocument();
  });

  it('excludes archived schedules from the board', async () => {
    vi.stubGlobal(
      'fetch',
      routedFetch({ list: { items: [schedule(), schedule({ schedule_id: 'SCH-OLD', name: 'Retired rotation', status: 'archived' })] } }),
    );
    renderApp();

    await screen.findByText('Primary on-call');
    expect(screen.queryByText('Retired rotation')).not.toBeInTheDocument();
  });

  it('degrades on-call/participant fetch failure without blanking the card', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [schedule()] }, onCall: 'ERROR', participants: 'ERROR' }));
    renderApp();

    await screen.findByText('Primary on-call');
    await waitFor(() => expect(screen.getAllByText('Could not load').length).toBe(2));
  });

  it('shows an explicit empty state with no schedules', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] } }));
    renderApp();

    expect(await screen.findByText('No on-call schedules have been created yet.')).toBeInTheDocument();
  });

  it('shows an explicit empty state when every schedule is archived', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [schedule({ status: 'archived' })] } }));
    renderApp();

    expect(await screen.findByText('No active on-call schedules.')).toBeInTheDocument();
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

// E-ACT.4 / ADR-ACT-005: create/edit a schedule and manage its roster
// (add/remove/reorder), against the real EXISTING E5.2a endpoints, with the
// add-participant picker backed by GET /v1/admin/tenant-users (E-ACT.0).
// A stateful fake — mutations here actually change what a subsequent GET
// returns — so "refetch shows the new state" is proven, not merely "the
// right body was sent" (the same discipline IncidentDetail.test.tsx's
// routedFetch establishes).

const priya: TenantUserDTO = { user_id: 'U-PRIYA', email: 'priya@example.com', display_name: 'Priya Nair' };
const raj: TenantUserDTO = { user_id: 'U-RAJ', email: 'raj@example.com', display_name: 'Raj Shah' };

interface MutableFixture {
  schedules?: OnCallScheduleDTO[];
  participants?: Record<string, OnCallParticipantDTO[]>;
  users?: TenantUserDTO[];
  createResult?: 'ERROR';
  patchResult?: 'CONFLICT' | 'ERROR';
  addResult?: 'ERROR';
}

function mutableFetch(fx: MutableFixture = {}) {
  const schedules: Record<string, OnCallScheduleDTO> = {};
  (fx.schedules ?? []).forEach((s) => (schedules[s.schedule_id] = { ...s }));
  const participants: Record<string, OnCallParticipantDTO[]> = {};
  Object.entries(fx.participants ?? {}).forEach(([k, v]) => (participants[k] = v.map((p) => ({ ...p }))));

  let scheduleSeq = 0;
  let participantSeq = 0;

  const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
  const fail = (status: number, body: Record<string, unknown>) => ({ ok: false, status, json: async () => ({ status, ...body }) });
  const displayNameFor = (userId: string) => ([priya, raj].find((u) => u.user_id === userId)?.display_name);

  return vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url);
    const method = init?.method ?? 'GET';

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });
    if (u.includes('/admin/tenant-users')) return ok({ items: fx.users ?? [] });

    const reorderMatch = u.match(/on-call-schedules\/([^/]+)\/participants\/reorder/);
    if (method === 'POST' && reorderMatch) {
      const id = reorderMatch[1];
      const body = JSON.parse(String(init?.body)) as { participant_ids: string[] };
      const byId = new Map((participants[id] ?? []).map((p) => [p.participant_id, p]));
      const reordered = body.participant_ids.map((pid, idx) => ({ ...byId.get(pid)!, position: idx }));
      participants[id] = reordered;
      return ok({ items: reordered });
    }

    const removeMatch = u.match(/on-call-schedules\/([^/]+)\/participants\/([^/?]+)$/);
    if (method === 'DELETE' && removeMatch) {
      const [, id, pid] = removeMatch;
      const list = (participants[id] ?? []).filter((p) => p.participant_id !== pid);
      list.forEach((p, i) => (p.position = i));
      participants[id] = list;
      return { ok: true, status: 204, json: async () => null };
    }

    const participantsBaseMatch = u.match(/on-call-schedules\/([^/]+)\/participants(\?|$)/);
    if (method === 'POST' && participantsBaseMatch) {
      const id = participantsBaseMatch[1];
      if (fx.addResult === 'ERROR') {
        return fail(404, { title: 'not found', detail: 'user_id does not name an active member of this tenant' });
      }
      const body = JSON.parse(String(init?.body)) as { user_id: string };
      const list = participants[id] ?? [];
      if (list.some((p) => p.user_id === body.user_id)) {
        return fail(409, { title: 'conflict', detail: 'user is already a participant on this schedule' });
      }
      const created: OnCallParticipantDTO = {
        participant_id: `PART-NEW-${++participantSeq}`,
        schedule_id: id,
        user_id: body.user_id,
        position: list.length,
        row_version: 1,
        created_at: '2026-08-06T00:00:00Z',
        updated_at: '2026-08-06T00:00:00Z',
      };
      participants[id] = [...list, created];
      return ok(created, 201);
    }
    if (method === 'GET' && participantsBaseMatch) {
      const id = participantsBaseMatch[1];
      return ok({ items: participants[id] ?? [] });
    }

    const onCallMatch = u.match(/on-call-schedules\/([^/]+)\/on-call(\?|$)/);
    if (method === 'GET' && onCallMatch) {
      const id = onCallMatch[1];
      const first = [...(participants[id] ?? [])].sort((a, b) => a.position - b.position)[0];
      const dto: OnCallNowDTO = {
        schedule_id: id,
        at: '2026-08-06T10:00:00Z',
        user_id: first?.user_id,
        display_name: first ? displayNameFor(first.user_id) : undefined,
      };
      return ok(dto);
    }

    const byIdMatch = u.match(/on-call-schedules\/([^/?]+)$/);
    if (method === 'PATCH' && byIdMatch) {
      const id = byIdMatch[1];
      if (fx.patchResult === 'CONFLICT') {
        return fail(409, { title: 'conflict', detail: 'on-call schedule was modified concurrently; re-read and retry' });
      }
      if (fx.patchResult === 'ERROR') return fail(422, { title: 'validation failed', detail: 'bad request' });
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      const current = schedules[id];
      const updated: OnCallScheduleDTO = {
        ...current,
        ...(typeof body.name === 'string' ? { name: body.name } : {}),
        ...(typeof body.handoff_interval_seconds === 'number' ? { handoff_interval_seconds: body.handoff_interval_seconds } : {}),
        ...(typeof body.rotation_start_at === 'string' ? { rotation_start_at: body.rotation_start_at } : {}),
        ...(typeof body.status === 'string' ? { status: body.status as OnCallScheduleDTO['status'] } : {}),
        row_version: current.row_version + 1,
      };
      schedules[id] = updated;
      return ok(updated);
    }
    if (method === 'GET' && byIdMatch) {
      const id = byIdMatch[1];
      const sch = schedules[id];
      if (!sch) return fail(404, { title: 'not found' });
      return ok(sch);
    }

    if (method === 'POST' && /\/admin\/on-call-schedules$/.test(u)) {
      if (fx.createResult === 'ERROR') return fail(422, { title: 'validation failed', detail: 'name must not be empty' });
      const body = JSON.parse(String(init?.body)) as {
        name: string;
        handoff_interval_seconds: number;
        rotation_start_at: string;
      };
      const created: OnCallScheduleDTO = {
        schedule_id: `SCH-NEW-${++scheduleSeq}`,
        name: body.name,
        handoff_interval_seconds: body.handoff_interval_seconds,
        rotation_start_at: body.rotation_start_at,
        status: 'active',
        row_version: 1,
        created_at: '2026-08-06T00:00:00Z',
        updated_at: '2026-08-06T00:00:00Z',
      };
      schedules[created.schedule_id] = created;
      participants[created.schedule_id] = [];
      return ok(created, 201);
    }

    if (method === 'GET' && u.includes('/admin/on-call-schedules')) {
      return ok({ items: Object.values(schedules) });
    }

    return ok({});
  });
}

/** Opens a schedule's detail (roster management) split panel from the board. */
async function openScheduleManagement(name: string) {
  fireEvent.click(await screen.findByRole('button', { name }));
  await screen.findByRole('heading', { name });
}

describe('create schedule', () => {
  it('is disabled until name/interval/rotation-start are all answered', async () => {
    vi.stubGlobal('fetch', mutableFetch({ schedules: [] }));
    renderApp();

    await screen.findByText('No on-call schedules have been created yet.');
    fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
  });

  it('posts the exact create body (with the friendly preset resolved to seconds) and opens the new schedule', async () => {
    const f = mutableFetch({ schedules: [] });
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByText('No on-call schedules have been created yet.');
    fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Schedule name'), { target: { value: 'Weekend rotation' } });
    act(() => createWrapper(dialog).findAllDateInputs()[0].setInputValue('2026/09/01'));
    act(() => createWrapper(dialog).findAllTimeInputs()[0].setInputValue('09:00'));

    await waitFor(() => expect(within(dialog).getByRole('button', { name: 'Create' })).not.toBeDisabled());

    // Default preset is "1 day" (86,400s) — no need to touch the interval Select.
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const createCall = f.mock.calls.find(
      ([url, init]) => String(url).endsWith('/admin/on-call-schedules') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    const expectedStart = combineDateAndTime('2026-09-01', '09:00')!.toISOString();
    expect(JSON.parse(String((createCall[1] as RequestInit).body))).toEqual({
      name: 'Weekend rotation',
      handoff_interval_seconds: 86_400,
      rotation_start_at: expectedStart,
    });

    // The new schedule's own detail opened in the split panel.
    expect(await screen.findByRole('heading', { name: 'Weekend rotation' })).toBeInTheDocument();
  });

  it('surfaces a validation error instead of closing the dialog', async () => {
    vi.stubGlobal('fetch', mutableFetch({ schedules: [], createResult: 'ERROR' }));
    renderApp();

    await screen.findByText('No on-call schedules have been created yet.');
    fireEvent.click(screen.getByRole('button', { name: 'Create schedule' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Schedule name'), { target: { value: 'Bad rotation' } });
    act(() => createWrapper(dialog).findAllDateInputs()[0].setInputValue('2026/09/01'));
    act(() => createWrapper(dialog).findAllTimeInputs()[0].setInputValue('09:00'));
    await waitFor(() => expect(within(dialog).getByRole('button', { name: 'Create' })).not.toBeDisabled());
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('name must not be empty');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});

describe('manage schedule roster', () => {
  const solo = schedule({ schedule_id: 'SCH-SOLO', name: 'Solo rotation', row_version: 3 });

  it('edit sends row_version and the new fields, then refetches', async () => {
    const f = mutableFetch({ schedules: [solo], participants: { 'SCH-SOLO': [] }, users: [priya, raj] });
    vi.stubGlobal('fetch', f);
    renderApp();

    await openScheduleManagement('Solo rotation');
    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Schedule name'), { target: { value: 'Solo rotation (renamed)' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const patchCall = f.mock.calls.find(
      ([url, init]) => String(url).endsWith('/on-call-schedules/SCH-SOLO') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    const body = JSON.parse(String((patchCall[1] as RequestInit).body));
    expect(body.row_version).toBe(3);
    expect(body.name).toBe('Solo rotation (renamed)');

    // The panel's own body reflects the rename via a refetch (the SplitPanel's
    // header itself is set once at open time by the board and does not
    // follow a rename — a documented, minor limitation, see ADR-ACT-005).
    // Both the board's own card title (reloaded via onChanged) and the
    // panel's own "Name" field reflect the rename; the SplitPanel's header
    // itself is set once at open time and does not follow it (documented,
    // minor limitation — see ADR-ACT-005).
    expect((await screen.findAllByText('Solo rotation (renamed)')).length).toBeGreaterThan(0);
  });

  it('on a 409 conflict: refetches and shows a notice, and never blindly retries the mutation', async () => {
    const f = mutableFetch({
      schedules: [solo],
      participants: { 'SCH-SOLO': [] },
      users: [],
      patchResult: 'CONFLICT',
    });
    vi.stubGlobal('fetch', f);
    renderApp();

    await openScheduleManagement('Solo rotation');
    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/Changed since you loaded it/);
    expect(f.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')).toHaveLength(1);
  });

  it('adds a participant via the tenant-users picker (excluding those already rostered), and posts user_id', async () => {
    const rostered = schedule({ schedule_id: 'SCH-ROSTER', name: 'Roster rotation' });
    const existingParticipant: OnCallParticipantDTO = {
      participant_id: 'PART-PRIYA',
      schedule_id: 'SCH-ROSTER',
      user_id: 'U-PRIYA',
      position: 0,
      row_version: 1,
      created_at: '2026-08-01T00:00:00Z',
      updated_at: '2026-08-01T00:00:00Z',
    };
    const f = mutableFetch({
      schedules: [rostered],
      participants: { 'SCH-ROSTER': [existingParticipant] },
      users: [priya, raj],
    });
    vi.stubGlobal('fetch', f);
    renderApp();

    await openScheduleManagement('Roster rotation');
    await waitFor(() => expect(screen.getAllByText('U-PRIYA').length).toBeGreaterThan(0));

    const addSelect = await waitFor(() => {
      const s = createWrapper()
        .findAllSelects()
        .find((sel) => sel.findTrigger().getElement().textContent?.includes('Choose a user'));
      if (!s) throw new Error('add-participant select not ready');
      return s;
    });
    act(() => addSelect.openDropdown());
    const optionTexts = addSelect.findDropdown().findOptions().map((o) => o.getElement().textContent ?? '');
    expect(optionTexts.some((t) => t.includes('Raj Shah'))).toBe(true);
    expect(optionTexts.some((t) => t.includes('Priya Nair'))).toBe(false);
    act(() => addSelect.selectOptionByValue('U-RAJ'));

    fireEvent.click(screen.getByRole('button', { name: 'Add' }));

    await waitFor(() => expect(screen.getAllByText('U-RAJ').length).toBeGreaterThan(0));
    const addCall = f.mock.calls.find(
      ([url, init]) =>
        String(url).endsWith('/on-call-schedules/SCH-ROSTER/participants') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    expect(JSON.parse(String((addCall[1] as RequestInit).body))).toEqual({ user_id: 'U-RAJ' });
  });

  it('reorders by sending the full ordered participant set (not a partial move)', async () => {
    const rostered = schedule({ schedule_id: 'SCH-ORDER', name: 'Order rotation' });
    const rosterParticipants: OnCallParticipantDTO[] = [
      { participant_id: 'PART-A', schedule_id: 'SCH-ORDER', user_id: 'U-PRIYA', position: 0, row_version: 1, created_at: 'x', updated_at: 'x' },
      { participant_id: 'PART-B', schedule_id: 'SCH-ORDER', user_id: 'U-RAJ', position: 1, row_version: 1, created_at: 'x', updated_at: 'x' },
    ];
    const f = mutableFetch({ schedules: [rostered], participants: { 'SCH-ORDER': rosterParticipants }, users: [priya, raj] });
    vi.stubGlobal('fetch', f);
    renderApp();

    await openScheduleManagement('Order rotation');
    await waitFor(() => expect(screen.getAllByText('U-PRIYA').length).toBeGreaterThan(0));

    fireEvent.click(screen.getByRole('button', { name: 'Move U-RAJ up' }));

    await waitFor(() => {
      const reorderCall = f.mock.calls.find(
        ([url, init]) =>
          String(url).includes('/on-call-schedules/SCH-ORDER/participants/reorder') &&
          (init as RequestInit | undefined)?.method === 'POST',
      );
      expect(reorderCall).toBeTruthy();
      expect(JSON.parse(String((reorderCall![1] as RequestInit).body))).toEqual({
        participant_ids: ['PART-B', 'PART-A'],
      });
    });
  });

  it('remove confirms, then deletes and refetches the roster', async () => {
    const rostered = schedule({ schedule_id: 'SCH-REMOVE', name: 'Remove rotation' });
    const rosterParticipants: OnCallParticipantDTO[] = [
      { participant_id: 'PART-A', schedule_id: 'SCH-REMOVE', user_id: 'U-PRIYA', position: 0, row_version: 1, created_at: 'x', updated_at: 'x' },
    ];
    const f = mutableFetch({ schedules: [rostered], participants: { 'SCH-REMOVE': rosterParticipants }, users: [priya] });
    vi.stubGlobal('fetch', f);
    renderApp();

    await openScheduleManagement('Remove rotation');
    await waitFor(() => expect(screen.getAllByText('U-PRIYA').length).toBeGreaterThan(0));

    fireEvent.click(screen.getByRole('button', { name: 'Remove U-PRIYA' }));
    const dialog = await screen.findByRole('dialog');
    expect(f.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Remove' }));

    await waitFor(() => expect(screen.queryAllByText('U-PRIYA').length).toBe(0));
    expect(f.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(true);
  });
});
