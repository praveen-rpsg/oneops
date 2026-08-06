import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { EscalationPolicyDTO, EscalationTierDTO } from '../escalation';
import type { OnCallScheduleDTO } from '../onCall';

const policy = (over: Partial<EscalationPolicyDTO> = {}): EscalationPolicyDTO => ({
  policy_id: 'POL-PRIMARY',
  name: 'Primary escalation',
  status: 'active',
  row_version: 1,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  ...over,
});

const primarySchedule: OnCallScheduleDTO = {
  schedule_id: 'SCH-PRIMARY',
  name: 'Primary rotation',
  handoff_interval_seconds: 86_400,
  rotation_start_at: '2026-08-01T00:00:00Z',
  status: 'active',
  row_version: 1,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
};

const secondSchedule: OnCallScheduleDTO = {
  schedule_id: 'SCH-SECOND',
  name: 'Secondary rotation',
  handoff_interval_seconds: 86_400,
  rotation_start_at: '2026-08-01T00:00:00Z',
  status: 'active',
  row_version: 1,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
};

interface Fixture {
  list?: unknown;
}

function routedFetch(fx: Fixture = {}) {
  return vi.fn().mockImplementation(async (url: string) => {
    const u = String(url);
    const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });
    if (u.includes('/on-call-schedules')) return ok({ items: [primarySchedule] });
    if (u.includes('/escalation-policies')) {
      if (fx.list === 'ERROR') {
        return { ok: false, status: 500, json: async () => ({ title: 'internal error', status: 500, detail: 'boom' }) };
      }
      return ok(fx.list ?? { items: [] });
    }

    return ok({});
  });
}

beforeEach(() => window.history.replaceState(null, '', '/escalation'));
afterEach(() => vi.unstubAllGlobals());

describe('escalation board', () => {
  it('renders the policy list', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [policy()] } }));
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Escalation/ })).toBeInTheDocument();
    expect(await screen.findByText('Primary escalation')).toBeInTheDocument();
  });

  it('shows an explicit empty state with no policies', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] } }));
    renderApp();

    expect(await screen.findByText('No escalation policies have been created yet.')).toBeInTheDocument();
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

// E-ACT.5 / ADR-ACT-006: create/edit a policy and manage its tier ladder
// (add/remove/reorder), against the real EXISTING E5.2b-1 endpoints, with the
// add-tier picker backed by GET /v1/admin/on-call-schedules. A stateful fake
// — mutations here actually change what a subsequent GET returns — so
// "refetch shows the new state" is proven, not merely "the right body was
// sent" (the same discipline OnCallBoardPage.test.tsx's mutableFetch
// establishes).

interface MutableFixture {
  policies?: EscalationPolicyDTO[];
  tiers?: Record<string, EscalationTierDTO[]>;
  schedules?: OnCallScheduleDTO[];
  createResult?: 'ERROR';
  patchResult?: 'CONFLICT' | 'ERROR';
  addResult?: 'ERROR';
}

function mutableFetch(fx: MutableFixture = {}) {
  const policies: Record<string, EscalationPolicyDTO> = {};
  (fx.policies ?? []).forEach((p) => (policies[p.policy_id] = { ...p }));
  const tiers: Record<string, EscalationTierDTO[]> = {};
  Object.entries(fx.tiers ?? {}).forEach(([k, v]) => (tiers[k] = v.map((t) => ({ ...t }))));
  const schedules = fx.schedules ?? [primarySchedule, secondSchedule];

  let policySeq = 0;
  let tierSeq = 0;

  const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
  const fail = (status: number, body: Record<string, unknown>) => ({ ok: false, status, json: async () => ({ status, ...body }) });

  return vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url);
    const method = init?.method ?? 'GET';

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });
    if (method === 'GET' && u.includes('/admin/on-call-schedules')) return ok({ items: schedules });

    const reorderMatch = u.match(/escalation-policies\/([^/]+)\/tiers\/reorder/);
    if (method === 'POST' && reorderMatch) {
      const id = reorderMatch[1];
      const body = JSON.parse(String(init?.body)) as { tier_ids: string[] };
      const byId = new Map((tiers[id] ?? []).map((t) => [t.tier_id, t]));
      const reordered = body.tier_ids.map((tid, idx) => ({ ...byId.get(tid)!, position: idx }));
      tiers[id] = reordered;
      return ok({ items: reordered });
    }

    const removeMatch = u.match(/escalation-policies\/([^/]+)\/tiers\/([^/?]+)$/);
    if (method === 'DELETE' && removeMatch) {
      const [, id, tid] = removeMatch;
      const list = (tiers[id] ?? []).filter((t) => t.tier_id !== tid);
      list.forEach((t, i) => (t.position = i));
      tiers[id] = list;
      return { ok: true, status: 204, json: async () => null };
    }

    const tiersBaseMatch = u.match(/escalation-policies\/([^/]+)\/tiers(\?|$)/);
    if (method === 'POST' && tiersBaseMatch) {
      const id = tiersBaseMatch[1];
      if (fx.addResult === 'ERROR') {
        return fail(404, { title: 'not found', detail: 'on_call_schedule_id does not name a schedule of this tenant' });
      }
      const body = JSON.parse(String(init?.body)) as { on_call_schedule_id: string; wait_seconds: number };
      const list = tiers[id] ?? [];
      const created: EscalationTierDTO = {
        tier_id: `TIER-NEW-${++tierSeq}`,
        policy_id: id,
        position: list.length,
        on_call_schedule_id: body.on_call_schedule_id,
        wait_seconds: body.wait_seconds,
        row_version: 1,
        created_at: '2026-08-06T00:00:00Z',
        updated_at: '2026-08-06T00:00:00Z',
      };
      tiers[id] = [...list, created];
      return ok(created, 201);
    }
    if (method === 'GET' && tiersBaseMatch) {
      const id = tiersBaseMatch[1];
      return ok({ items: tiers[id] ?? [] });
    }

    const byIdMatch = u.match(/escalation-policies\/([^/?]+)$/);
    if (method === 'PATCH' && byIdMatch) {
      const id = byIdMatch[1];
      if (fx.patchResult === 'CONFLICT') {
        return fail(409, { title: 'conflict', detail: 'escalation policy was modified concurrently; re-read and retry' });
      }
      if (fx.patchResult === 'ERROR') return fail(422, { title: 'validation failed', detail: 'bad request' });
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      const current = policies[id];
      const updated: EscalationPolicyDTO = {
        ...current,
        ...(typeof body.name === 'string' ? { name: body.name } : {}),
        ...(typeof body.status === 'string' ? { status: body.status as EscalationPolicyDTO['status'] } : {}),
        row_version: current.row_version + 1,
      };
      policies[id] = updated;
      return ok(updated);
    }
    if (method === 'GET' && byIdMatch) {
      const id = byIdMatch[1];
      const p = policies[id];
      if (!p) return fail(404, { title: 'not found' });
      return ok(p);
    }

    if (method === 'POST' && /\/admin\/escalation-policies$/.test(u)) {
      if (fx.createResult === 'ERROR') return fail(422, { title: 'validation failed', detail: 'name must not be empty' });
      const body = JSON.parse(String(init?.body)) as { name: string };
      const created: EscalationPolicyDTO = {
        policy_id: `POL-NEW-${++policySeq}`,
        name: body.name,
        status: 'active',
        row_version: 1,
        created_at: '2026-08-06T00:00:00Z',
        updated_at: '2026-08-06T00:00:00Z',
      };
      policies[created.policy_id] = created;
      tiers[created.policy_id] = [];
      return ok(created, 201);
    }

    if (method === 'GET' && u.includes('/admin/escalation-policies')) {
      return ok({ items: Object.values(policies) });
    }

    return ok({});
  });
}

/** Opens a policy's tier-management split panel from the board. */
async function openPolicyManagement(name: string) {
  fireEvent.click((await screen.findAllByRole('button', { name }))[0]);
  await screen.findByRole('heading', { name });
}

describe('create policy', () => {
  it('is disabled until name is answered', async () => {
    vi.stubGlobal('fetch', mutableFetch({ policies: [] }));
    renderApp();

    await screen.findByText('No escalation policies have been created yet.');
    fireEvent.click(screen.getByRole('button', { name: 'Create policy' }));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
  });

  it('posts the exact create body and opens the new policy', async () => {
    const f = mutableFetch({ policies: [] });
    vi.stubGlobal('fetch', f);
    renderApp();

    await screen.findByText('No escalation policies have been created yet.');
    fireEvent.click(screen.getByRole('button', { name: 'Create policy' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Policy name'), { target: { value: 'Weekend escalation' } });
    await waitFor(() => expect(within(dialog).getByRole('button', { name: 'Create' })).not.toBeDisabled());
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const createCall = f.mock.calls.find(
      ([url, init]) => String(url).endsWith('/admin/escalation-policies') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    expect(JSON.parse(String((createCall[1] as RequestInit).body))).toEqual({ name: 'Weekend escalation' });

    expect(await screen.findByRole('heading', { name: 'Weekend escalation' })).toBeInTheDocument();
  });

  it('surfaces a validation error instead of closing the dialog', async () => {
    vi.stubGlobal('fetch', mutableFetch({ policies: [], createResult: 'ERROR' }));
    renderApp();

    await screen.findByText('No escalation policies have been created yet.');
    fireEvent.click(screen.getByRole('button', { name: 'Create policy' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Policy name'), { target: { value: 'Bad policy' } });
    await waitFor(() => expect(within(dialog).getByRole('button', { name: 'Create' })).not.toBeDisabled());
    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('name must not be empty');
    expect(screen.getByRole('dialog')).toBeInTheDocument();
  });
});

describe('manage policy tiers', () => {
  const solo = policy({ policy_id: 'POL-SOLO', name: 'Solo escalation', row_version: 3 });

  it('edit sends row_version and the new fields, then refetches', async () => {
    const f = mutableFetch({ policies: [solo], tiers: { 'POL-SOLO': [] } });
    vi.stubGlobal('fetch', f);
    renderApp();

    await openPolicyManagement('Solo escalation');
    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Policy name'), { target: { value: 'Solo escalation (renamed)' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const patchCall = f.mock.calls.find(
      ([url, init]) => String(url).endsWith('/escalation-policies/POL-SOLO') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    const body = JSON.parse(String((patchCall[1] as RequestInit).body));
    expect(body.row_version).toBe(3);
    expect(body.name).toBe('Solo escalation (renamed)');

    expect((await screen.findAllByText('Solo escalation (renamed)')).length).toBeGreaterThan(0);
  });

  it('on a 409 conflict: refetches and shows a notice, and never blindly retries the mutation', async () => {
    const f = mutableFetch({ policies: [solo], tiers: { 'POL-SOLO': [] }, patchResult: 'CONFLICT' });
    vi.stubGlobal('fetch', f);
    renderApp();

    await openPolicyManagement('Solo escalation');
    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/Changed since you loaded it/);
    expect(f.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')).toHaveLength(1);
  });

  it('adds a tier via the on-call-schedule picker, and posts on_call_schedule_id + wait_seconds', async () => {
    const withTiers = policy({ policy_id: 'POL-TIERS', name: 'Tiered escalation' });
    const f = mutableFetch({ policies: [withTiers], tiers: { 'POL-TIERS': [] } });
    vi.stubGlobal('fetch', f);
    renderApp();

    await openPolicyManagement('Tiered escalation');
    await screen.findByText('No tiers yet.');

    const scheduleSelect = await waitFor(() => {
      const s = createWrapper()
        .findAllSelects()
        .find((sel) => sel.findTrigger().getElement().textContent?.includes('Choose a schedule'));
      if (!s) throw new Error('add-tier schedule select not ready');
      return s;
    });
    act(() => scheduleSelect.openDropdown());
    const optionTexts = scheduleSelect.findDropdown().findOptions().map((o) => o.getElement().textContent ?? '');
    expect(optionTexts.some((t) => t.includes('Primary rotation'))).toBe(true);
    expect(optionTexts.some((t) => t.includes('Secondary rotation'))).toBe(true);
    act(() => scheduleSelect.selectOptionByValue('SCH-SECOND'));

    fireEvent.click(screen.getByRole('button', { name: 'Add tier' }));

    await waitFor(() => expect(screen.getAllByText('Secondary rotation').length).toBeGreaterThan(0));
    const addCall = f.mock.calls.find(
      ([url, init]) =>
        String(url).endsWith('/escalation-policies/POL-TIERS/tiers') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    // Default wait preset is "5 minutes" (300s) — no need to touch the wait Select.
    expect(JSON.parse(String((addCall[1] as RequestInit).body))).toEqual({
      on_call_schedule_id: 'SCH-SECOND',
      wait_seconds: 300,
    });
  });

  it('reorders by sending the full ordered tier set (not a partial move)', async () => {
    const withTiers = policy({ policy_id: 'POL-ORDER', name: 'Order escalation' });
    const orderedTiers: EscalationTierDTO[] = [
      { tier_id: 'TIER-A', policy_id: 'POL-ORDER', position: 0, on_call_schedule_id: 'SCH-PRIMARY', wait_seconds: 300, row_version: 1, created_at: 'x', updated_at: 'x' },
      { tier_id: 'TIER-B', policy_id: 'POL-ORDER', position: 1, on_call_schedule_id: 'SCH-SECOND', wait_seconds: 600, row_version: 1, created_at: 'x', updated_at: 'x' },
    ];
    const f = mutableFetch({ policies: [withTiers], tiers: { 'POL-ORDER': orderedTiers } });
    vi.stubGlobal('fetch', f);
    renderApp();

    await openPolicyManagement('Order escalation');
    await waitFor(() => expect(screen.getAllByText('Secondary rotation').length).toBeGreaterThan(0));

    fireEvent.click(screen.getByRole('button', { name: 'Move tier 2 up' }));

    await waitFor(() => {
      const reorderCall = f.mock.calls.find(
        ([url, init]) =>
          String(url).includes('/escalation-policies/POL-ORDER/tiers/reorder') &&
          (init as RequestInit | undefined)?.method === 'POST',
      );
      expect(reorderCall).toBeTruthy();
      expect(JSON.parse(String((reorderCall![1] as RequestInit).body))).toEqual({
        tier_ids: ['TIER-B', 'TIER-A'],
      });
    });
  });

  it('remove confirms, then deletes and refetches the ladder', async () => {
    const withTiers = policy({ policy_id: 'POL-REMOVE', name: 'Remove escalation' });
    const oneTier: EscalationTierDTO[] = [
      { tier_id: 'TIER-A', policy_id: 'POL-REMOVE', position: 0, on_call_schedule_id: 'SCH-PRIMARY', wait_seconds: 300, row_version: 1, created_at: 'x', updated_at: 'x' },
    ];
    const f = mutableFetch({ policies: [withTiers], tiers: { 'POL-REMOVE': oneTier } });
    vi.stubGlobal('fetch', f);
    renderApp();

    await openPolicyManagement('Remove escalation');
    await waitFor(() => expect(screen.getAllByText('Primary rotation').length).toBeGreaterThan(0));

    fireEvent.click(screen.getByRole('button', { name: 'Remove tier 1' }));
    const dialog = await screen.findByRole('dialog');
    expect(f.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Remove' }));

    await waitFor(() => expect(screen.queryByText('No tiers yet.')).toBeInTheDocument());
    expect(f.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(true);
  });

  it('surfaces an add-tier error as an inline business conflict, not a stale-read reload', async () => {
    const withTiers = policy({ policy_id: 'POL-ADDERR', name: 'Add-error escalation' });
    const f = mutableFetch({ policies: [withTiers], tiers: { 'POL-ADDERR': [] }, addResult: 'ERROR' });
    vi.stubGlobal('fetch', f);
    renderApp();

    await openPolicyManagement('Add-error escalation');
    await screen.findByText('No tiers yet.');

    const scheduleSelect = await waitFor(() => {
      const s = createWrapper()
        .findAllSelects()
        .find((sel) => sel.findTrigger().getElement().textContent?.includes('Choose a schedule'));
      if (!s) throw new Error('add-tier schedule select not ready');
      return s;
    });
    act(() => scheduleSelect.openDropdown());
    act(() => scheduleSelect.selectOptionByValue('SCH-PRIMARY'));
    fireEvent.click(screen.getByRole('button', { name: 'Add tier' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/does not name a schedule/);
    expect(screen.getByText('No tiers yet.')).toBeInTheDocument();
  });
});
