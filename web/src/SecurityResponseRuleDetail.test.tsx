import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from './test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { SecurityResponseRuleDTO } from './securityResponseRules';

// Exercises E-SEC-UI.4's write actions (Edit, Enable/Disable, Delete) through
// the real board -> split-panel integration, the same way
// SecurityRuleDetail.test.tsx drives ADR-ACT-002's actions — row_version
// round-trip on Edit/Enable-Disable, 409-triggers-refetch, the confirmed
// no-row_version-on-delete contract, and error surfacing.

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

interface Fixture {
  list?: unknown;
  initial: SecurityResponseRuleDTO;
  patchResult?: 'OK' | 'CONFLICT' | 'ERROR';
  deleteResult?: 'OK' | 'ERROR';
}

/**
 * A tiny, stateful fake of the security-response-rule admin API: PATCH
 * mutates an in-memory row so a subsequent GET (the refetch every action
 * triggers) reflects the change; DELETE clears it.
 */
function routedFetch(fx: Fixture) {
  const detail: Record<string, SecurityResponseRuleDTO> = { [fx.initial.rule_id]: fx.initial };
  const ok = (body: unknown, status = 200) => ({ ok: true, status, json: async () => body });
  const fail = (status: number, body: Record<string, unknown>) => ({ ok: false, status, json: async () => ({ status, ...body }) });

  return vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url);
    const method = init?.method ?? 'GET';

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    const idMatch = u.match(/security-response-rules\/([^/?]+)/);

    if (method === 'PATCH' && idMatch) {
      const id = idMatch[1];
      if (fx.patchResult === 'CONFLICT') {
        return fail(409, { title: 'conflict', detail: 'security response rule was modified concurrently; re-read and retry' });
      }
      if (fx.patchResult === 'ERROR') {
        return fail(422, { title: 'validation failed', detail: 'name must be at most 200 characters' });
      }
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      const current = detail[id];
      const updated: SecurityResponseRuleDTO = {
        ...current,
        ...(body.name !== undefined ? { name: body.name as string } : {}),
        ...(body.min_severity !== undefined ? { min_severity: body.min_severity as SecurityResponseRuleDTO['min_severity'] } : {}),
        ...(body.action_config !== undefined ? { action_config: body.action_config as Record<string, unknown> } : {}),
        ...(body.enabled !== undefined ? { enabled: body.enabled as boolean } : {}),
        row_version: current.row_version + 1,
      };
      detail[id] = updated;
      return ok(updated);
    }

    if (method === 'DELETE' && idMatch) {
      const id = idMatch[1];
      if (fx.deleteResult === 'ERROR') return fail(404, { title: 'not found', detail: 'no such security response rule' });
      delete detail[id];
      return { ok: true, status: 204, json: async () => undefined };
    }

    if (idMatch) {
      const body = detail[idMatch[1]];
      if (!body) return fail(404, { title: 'not found' });
      return ok(body);
    }

    if (u.includes('/admin/security-response-rules')) return ok(fx.list ?? { items: [fx.initial] });

    return ok({});
  });
}

async function openPanel() {
  fireEvent.click(await screen.findByRole('button', { name: 'Notify SOC on critical malware' }));
  await screen.findByRole('button', { name: 'Edit' });
}

beforeEach(() => window.history.replaceState(null, '', '/security/response-rules'));
afterEach(() => vi.unstubAllGlobals());

describe('response rule detail actions', () => {
  it('shows the SAFE-boundary notice in the detail panel', async () => {
    vi.stubGlobal('fetch', routedFetch({ initial: rule() }));
    renderApp();
    await openPanel();

    expect(screen.getByText(/SAFE action only/)).toBeInTheDocument();
    expect(screen.getByText(/cannot act on the incident's asset/)).toBeInTheDocument();
  });

  it('edits the rule: sends row_version + the patched fields, then refetches to show the new values', async () => {
    const f = routedFetch({ initial: rule({ row_version: 1 }) });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Name'), { target: { value: 'Notify SOC on any malware' } });

    const minSeveritySelect = createWrapper(dialog).findAllSelects()[0];
    act(() => minSeveritySelect.openDropdown());
    act(() => minSeveritySelect.selectOptionByValue('critical'));

    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    await waitFor(() => expect(screen.getByText('Notify SOC on any malware')).toBeInTheDocument());

    const patchCall = f.mock.calls.find(
      ([u, init]) => String(u).includes('/security-response-rules/RESP-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    expect(JSON.parse(String((patchCall[1] as RequestInit).body))).toEqual({
      row_version: 1,
      name: 'Notify SOC on any malware',
      min_severity: 'critical',
      action_config: { url: 'https://example.com/hooks/security' },
      enabled: true,
    });
  });

  it('does not offer asset_id/action_type as editable — they are fixed at creation', async () => {
    vi.stubGlobal('fetch', routedFetch({ initial: rule() }));
    renderApp();
    await openPanel();

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).queryByLabelText('Asset ID')).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText('Action type')).not.toBeInTheDocument();
    expect(dialog).toHaveTextContent(/AST-DB-1.*Http.*not editable/);
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

    expect(
      f.mock.calls.filter(
        ([u, init]) => String(u).includes('/security-response-rules/RESP-1') && (init as RequestInit | undefined)?.method === 'PATCH',
      ),
    ).toHaveLength(1);
    await waitFor(() =>
      expect(
        f.mock.calls.filter(
          ([u, init]) => /security-response-rules\/RESP-1$/.test(String(u)) && (init as RequestInit | undefined)?.method === undefined,
        ).length,
      ).toBeGreaterThan(1),
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
      ([u, init]) => String(u).includes('/security-response-rules/RESP-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    expect(JSON.parse(String((patchCall[1] as RequestInit).body))).toEqual({ row_version: 1, enabled: false });
  });

  it('deletes with a confirmation modal first, sends no row_version (the confirmed contract), then closes the panel and refetches the board', async () => {
    const f = routedFetch({ initial: rule() });
    vi.stubGlobal('fetch', f);
    renderApp();
    await openPanel();

    const listCallsBefore = f.mock.calls.filter(([u]) => String(u).includes('/admin/security-response-rules?')).length;

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent(/permanently removes the rule/);
    expect(f.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument());

    const deleteCall = f.mock.calls.find(([, init]) => (init as RequestInit | undefined)?.method === 'DELETE')!;
    expect(deleteCall[1]).not.toHaveProperty('body');

    await waitFor(() =>
      expect(f.mock.calls.filter(([u]) => String(u).includes('/admin/security-response-rules?')).length).toBeGreaterThan(listCallsBefore),
    );
  });

  it('surfaces a delete error inline rather than failing silently', async () => {
    vi.stubGlobal('fetch', routedFetch({ initial: rule(), deleteResult: 'ERROR' }));
    renderApp();
    await openPanel();

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('no such security response rule');
    expect(screen.getByRole('button', { name: 'Edit' })).toBeInTheDocument();
  });
});
