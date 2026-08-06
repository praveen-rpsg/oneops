import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from './test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { AlertRuleDTO } from './alertRules';

// Exercises E-ACT.2/ADR-ACT-002's write actions (Edit, Enable/Disable,
// Delete) through the real board -> split-panel integration, the same way
// IncidentDetail.test.tsx drives E-ACT.1's actions — row_version round-trip
// on Edit/Enable-Disable, 409-triggers-refetch, the confirmed
// no-row_version-on-delete contract, and error surfacing.

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

interface Fixture {
  list?: unknown;
  initial: AlertRuleDTO;
  /** 'OK' (default) applies the requested change; 'CONFLICT'/'ERROR' short-circuit the patch call. */
  patchResult?: 'OK' | 'CONFLICT' | 'ERROR';
  /** 'OK' (default) actually removes the row; 'ERROR' fails the delete call. */
  deleteResult?: 'OK' | 'ERROR';
}

/**
 * A tiny, stateful fake of the alert-rule admin API: PATCH mutates an
 * in-memory row so a subsequent GET (the refetch every action triggers)
 * reflects the change; DELETE clears it.
 */
function routedFetch(fx: Fixture) {
  const detail: Record<string, AlertRuleDTO> = { [fx.initial.rule_id]: fx.initial };
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

    const idMatch = u.match(/alert-rules\/([^/?]+)/);

    if (method === 'PATCH' && idMatch) {
      const id = idMatch[1];
      if (fx.patchResult === 'CONFLICT') {
        return fail(409, { title: 'conflict', detail: 'alert rule was modified concurrently; re-read and retry' });
      }
      if (fx.patchResult === 'ERROR') {
        return fail(422, { title: 'validation failed', detail: 'threshold must be a finite number' });
      }
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      const current = detail[id];
      const updated: AlertRuleDTO = {
        ...current,
        ...(body.comparator !== undefined ? { comparator: body.comparator as AlertRuleDTO['comparator'] } : {}),
        ...(body.threshold !== undefined ? { threshold: body.threshold as number } : {}),
        ...(body.for_duration_seconds !== undefined ? { for_duration_seconds: body.for_duration_seconds as number } : {}),
        ...(body.severity !== undefined ? { severity: body.severity as AlertRuleDTO['severity'] } : {}),
        ...(body.symptom_class !== undefined ? { symptom_class: body.symptom_class as AlertRuleDTO['symptom_class'] } : {}),
        ...(body.enabled !== undefined ? { enabled: body.enabled as boolean } : {}),
        ...(body.flap_dwell_seconds !== undefined ? { flap_dwell_seconds: body.flap_dwell_seconds as number } : {}),
        row_version: current.row_version + 1,
      };
      detail[id] = updated;
      return ok(updated);
    }

    if (method === 'DELETE' && idMatch) {
      const id = idMatch[1];
      if (fx.deleteResult === 'ERROR') {
        return fail(404, { title: 'not found', detail: 'no such alert rule' });
      }
      delete detail[id];
      return { ok: true, status: 204, json: async () => undefined };
    }

    if (idMatch) {
      const body = detail[idMatch[1]];
      if (!body) return fail(404, { title: 'not found' });
      return ok(body);
    }

    if (u.includes('/admin/alert-rules')) return ok(fx.list ?? { items: [fx.initial] });

    return ok({});
  });
}

async function openPanel() {
  fireEvent.click(await screen.findByRole('button', { name: /AST-WEB-1 · cpu_utilization/ }));
  await screen.findByRole('button', { name: 'Edit' });
}

beforeEach(() => window.history.replaceState(null, '', '/alerts'));
afterEach(() => vi.unstubAllGlobals());

describe('alert rule detail actions', () => {
  it('edits the rule: sends row_version + the patched fields, then refetches to show the new values', async () => {
    const f = routedFetch({ initial: rule({ row_version: 1 }) });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Threshold'), { target: { value: '95' } });

    const severitySelect = createWrapper(dialog).findAllSelects()[1];
    act(() => severitySelect.openDropdown());
    act(() => severitySelect.selectOptionByValue('critical'));

    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    await waitFor(() => expect(screen.getByText('> 95')).toBeInTheDocument());

    const patchCall = f.mock.calls.find(
      ([u, init]) => String(u).includes('/alert-rules/RULE-CPU') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    expect(JSON.parse(String((patchCall[1] as RequestInit).body))).toEqual({
      row_version: 1,
      comparator: 'gt',
      threshold: 95,
      for_duration_seconds: 300,
      severity: 'critical',
      symptom_class: 'resource',
      enabled: true,
      flap_dwell_seconds: 60,
    });
  });

  it('does not offer asset_id/metric as editable — they are fixed at creation', async () => {
    vi.stubGlobal('fetch', routedFetch({ initial: rule() }));
    renderApp();
    await openPanel();

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).queryByLabelText('Asset ID')).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText('Metric')).not.toBeInTheDocument();
    expect(dialog).toHaveTextContent(/AST-WEB-1.*cpu_utilization.*not editable/);
  });

  it('on a 409 conflict while editing: closes the dialog, refetches, and shows a notice without retrying', async () => {
    const f = routedFetch({ initial: rule({ row_version: 1 }), patchResult: 'CONFLICT' });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(/Changed since you loaded it/);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();

    expect(f.mock.calls.filter(([u, init]) => String(u).includes('/alert-rules/RULE-CPU') && (init as RequestInit | undefined)?.method === 'PATCH')).toHaveLength(1);
    await waitFor(() =>
      expect(f.mock.calls.filter(([u, init]) => /alert-rules\/RULE-CPU$/.test(String(u)) && (init as RequestInit | undefined)?.method === undefined).length).toBeGreaterThan(1),
    );
  });

  it('enables/disables with row_version, then refetches', async () => {
    const f = routedFetch({ initial: rule({ enabled: true, row_version: 1 }) });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    fireEvent.click(screen.getByRole('button', { name: 'Disable' }));

    await waitFor(() => expect(screen.getByRole('button', { name: 'Enable' })).toBeInTheDocument());

    const patchCall = f.mock.calls.find(
      ([u, init]) => String(u).includes('/alert-rules/RULE-CPU') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    expect(JSON.parse(String((patchCall[1] as RequestInit).body))).toEqual({ row_version: 1, enabled: false });
  });

  it('deletes with a confirmation modal first, sends no row_version (the confirmed contract), then closes the panel and refetches the board', async () => {
    const f = routedFetch({ initial: rule() });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    const listCallsBefore = f.mock.calls.filter(([u]) => String(u).includes('/admin/alert-rules?')).length;

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent(/permanently removes the rule/);
    expect(f.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    // The split panel closed — the "Edit" action from the now-deleted rule's detail is gone.
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument());

    const deleteCall = f.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')!;
    expect(deleteCall[1]).not.toHaveProperty('body');

    await waitFor(() =>
      expect(f.mock.calls.filter(([u]) => String(u).includes('/admin/alert-rules?')).length).toBeGreaterThan(
        listCallsBefore,
      ),
    );
  });

  it('surfaces a delete error inline rather than failing silently', async () => {
    vi.stubGlobal('fetch', routedFetch({ initial: rule(), deleteResult: 'ERROR' }));
    renderApp();
    await openPanel();

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('no such alert rule');
    // The rule is still here — no silent removal on a failed delete.
    expect(screen.getByRole('button', { name: 'Edit' })).toBeInTheDocument();
  });
});
