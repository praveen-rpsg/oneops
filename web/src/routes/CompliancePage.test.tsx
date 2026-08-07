import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { ComplianceControlDTO, ComplianceControlWithEvidenceDTO, ControlEvidenceDTO } from '../complianceControls';

// Exercises E-SEC-UI.3's Compliance screen: list + filter (GET
// /v1/admin/compliance-controls), "Register control" (POST
// .../compliance-controls) with framework/control_ref fixed at creation,
// legal-only implementation-lifecycle transitions and title/description Edit
// (PATCH .../{id}, internal/domain/compliance_control.go's
// complianceControlTransitions), the append-only evidence trail rendered
// newest-first with "Add evidence" (POST .../{id}/evidence), 409-refetches-
// with-notice, and the 403-graceful permission state.

const control = (over: Partial<ComplianceControlDTO> = {}): ComplianceControlDTO => ({
  control_id: 'CTRL-1',
  framework: 'SOC2',
  control_ref: 'CC6.1',
  title: 'Logical access controls',
  description: 'Restrict logical access to production systems.',
  status: 'not_implemented',
  row_version: 1,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-05T00:00:00Z',
  ...over,
});

const inProgressControl = control({
  control_id: 'CTRL-2',
  framework: 'ISO27001',
  control_ref: 'A.9.2.3',
  title: 'Access provisioning review',
  status: 'in_progress',
});

const evidenceOldestFirst: ControlEvidenceDTO[] = [
  { evidence_id: 'EV-1', kind: 'note', value: 'Kickoff review scheduled.', recorded_by: 'alice', recorded_at: '2026-08-01T00:00:00Z' },
  { evidence_id: 'EV-2', kind: 'url', value: 'https://evidence.example/scan-report', recorded_by: 'bob', recorded_at: '2026-08-03T00:00:00Z' },
];

interface Fixture {
  items?: ComplianceControlDTO[];
  evidenceByControl?: Record<string, ControlEvidenceDTO[]>;
  /** GET /admin/compliance-controls answers 403 — not a tenant admin. */
  forbidden?: boolean;
  /** PATCH .../{id} fails 409. */
  transitionConflict?: boolean;
}

function routedFetch(fx: Fixture = {}) {
  const items = [...(fx.items ?? [control()])];
  const evidence: Record<string, ControlEvidenceDTO[]> = { ...(fx.evidenceByControl ?? {}) };

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

    if (method === 'POST' && /\/admin\/compliance-controls$/.test(u)) {
      const body = JSON.parse(String(init?.body)) as Partial<ComplianceControlDTO>;
      const created = control({
        control_id: 'CTRL-NEW',
        framework: body.framework ?? '',
        control_ref: body.control_ref ?? '',
        title: body.title ?? '',
        description: body.description ?? '',
        status: 'not_implemented',
      });
      items.push(created);
      return ok(created, 201);
    }

    const evidenceMatch = u.match(/\/admin\/compliance-controls\/([^/?]+)\/evidence$/);
    if (method === 'POST' && evidenceMatch) {
      const id = evidenceMatch[1];
      const body = JSON.parse(String(init?.body)) as { kind: string; value: string };
      const record: ControlEvidenceDTO = {
        evidence_id: `EV-NEW-${(evidence[id]?.length ?? 0) + 1}`,
        kind: body.kind as ControlEvidenceDTO['kind'],
        value: body.value,
        recorded_by: 'test-actor',
        recorded_at: '2026-08-06T12:00:00Z',
      };
      evidence[id] = [...(evidence[id] ?? []), record];
      return ok(record, 201);
    }

    const patchMatch = u.match(/\/admin\/compliance-controls\/([^/?]+)$/);
    if (method === 'PATCH' && patchMatch) {
      if (fx.transitionConflict) {
        return fail(409, { title: 'conflict', detail: 'compliance control was modified concurrently; re-read and retry' });
      }
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      const i = items.findIndex((it) => it.control_id === patchMatch[1]);
      if (i < 0) return fail(404, { title: 'not found' });
      const { row_version: _rv, ...rest } = body;
      items[i] = { ...items[i], ...rest, row_version: items[i].row_version + 1 } as ComplianceControlDTO;
      return ok(items[i]);
    }

    if (patchMatch && method === 'GET') {
      const one = items.find((it) => it.control_id === patchMatch[1]);
      if (!one) return fail(404, { title: 'not found' });
      const withEvidence: ComplianceControlWithEvidenceDTO = { ...one, evidence: evidence[one.control_id] ?? [] };
      return ok(withEvidence);
    }

    if (u.includes('/admin/compliance-controls')) {
      if (fx.forbidden) return fail(403, { title: 'forbidden', detail: 'missing permission: admin' });
      return ok({ items });
    }

    return ok({});
  });
}

beforeEach(() => {
  window.history.replaceState(null, '', '/security/compliance');
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe('compliance board', () => {
  it('renders controls from a mocked list', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [control(), inProgressControl] }));
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Compliance/ })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'CC6.1' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'A.9.2.3' })).toBeInTheDocument();
  });

  it('filters by status', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [control(), inProgressControl] }));
    const { container } = renderApp();

    await screen.findByRole('button', { name: 'CC6.1' });

    const statusSelect = createWrapper(container).findAllSelects()[0];
    act(() => statusSelect.openDropdown());
    act(() => statusSelect.selectOptionByValue('in_progress'));

    await waitFor(() => expect(screen.queryByRole('button', { name: 'CC6.1' })).not.toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'A.9.2.3' })).toBeInTheDocument();
  });

  it('renders the clean permission-needed state on a 403, not a crash', async () => {
    vi.stubGlobal('fetch', routedFetch({ forbidden: true }));
    renderApp();

    expect(
      await screen.findByText('You need tenant-admin permission to manage compliance controls.'),
    ).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });
});

describe('create control', () => {
  it('is disabled until framework/control_ref/title are filled', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [control()] }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Register control' }));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
  });

  it('posts the exact create body and opens the new control', async () => {
    const f = routedFetch({ items: [control()] });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Register control' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Framework'), { target: { value: 'PCI-DSS' } });
    fireEvent.change(within(dialog).getByLabelText('Control ref'), { target: { value: '3.4.1' } });
    fireEvent.change(within(dialog).getByLabelText('Title'), { target: { value: 'Encrypt cardholder data' } });

    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const createCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/compliance-controls') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    expect(JSON.parse(String((createCall[1] as RequestInit).body))).toEqual({
      framework: 'PCI-DSS',
      control_ref: '3.4.1',
      title: 'Encrypt cardholder data',
    });

    expect(await screen.findByRole('heading', { name: 'PCI-DSS 3.4.1' })).toBeInTheDocument();
  });

  it('rejects an illegal framework before ever sending a request', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [control()] }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Register control' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Framework'), { target: { value: '1NVALID framework' } });
    fireEvent.change(within(dialog).getByLabelText('Control ref'), { target: { value: 'CC1' } });
    fireEvent.change(within(dialog).getByLabelText('Title'), { target: { value: 'x' } });

    expect(await within(dialog).findByText(/Must start with a letter/)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
  });
});

describe('control detail — status transitions', () => {
  it('offers only legal next statuses (not_implemented vs. in_progress)', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [control(), inProgressControl] }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'CC6.1' }));
    await screen.findByRole('button', { name: 'Start implementation' });
    expect(screen.getByRole('button', { name: 'Mark not applicable' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Mark implemented' })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'A.9.2.3' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Mark implemented' })).toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'Mark not applicable' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Reset to not implemented' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Start implementation' })).not.toBeInTheDocument();
  });

  it('not_applicable is a judgment: it requires confirmation, then patches row_version + status and refetches', async () => {
    const f = routedFetch({ items: [control()] });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'CC6.1' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Mark not applicable' }));

    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('not applicable to your environment');
    expect(f.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Mark not applicable' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const patchCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/compliance-controls/CTRL-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    expect(JSON.parse(String((patchCall[1] as RequestInit).body))).toEqual({ row_version: 1, status: 'not_applicable' });

    await waitFor(() => expect(screen.getAllByText('Not applicable').length).toBeGreaterThan(0));
  });

  it('on 409, refetches with a conflict notice and does not blindly retry', async () => {
    const f = routedFetch({ items: [control()], transitionConflict: true });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'CC6.1' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Start implementation' }));

    expect(await screen.findByText('Changed since you loaded it')).toBeInTheDocument();

    const patchCalls = f.mock.calls.filter(
      ([u, init]) => String(u).endsWith('/admin/compliance-controls/CTRL-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    );
    expect(patchCalls.length).toBe(1);
    // Still Not implemented — the conflicting move was never applied.
    expect(screen.getAllByText('Not implemented').length).toBeGreaterThan(0);
  });
});

describe('control detail — evidence trail', () => {
  it('renders the trail newest first, and appends via the add-evidence form', async () => {
    const f = routedFetch({ items: [control()], evidenceByControl: { 'CTRL-1': evidenceOldestFirst } });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'CC6.1' }));
    await screen.findByText('Kickoff review scheduled.');

    const trail = screen.getByRole('list', { name: 'Evidence trail' });
    const items = within(trail).getAllByRole('listitem');
    // Newest (EV-2, the URL) first, oldest (EV-1, the note) last — the
    // server itself returns oldest-first (see complianceControls.ts'
    // ComplianceControlWithEvidenceDTO doc comment); the console reverses it.
    expect(within(items[0]).getByText('https://evidence.example/scan-report')).toBeInTheDocument();
    expect(within(items[1]).getByText('Kickoff review scheduled.')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Evidence value'), { target: { value: 'Q3 access review completed.' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add evidence' }));

    await waitFor(() => expect(screen.getByText('Q3 access review completed.')).toBeInTheDocument());

    const evidenceCall = f.mock.calls.find(
      ([u, init]) =>
        String(u).endsWith('/admin/compliance-controls/CTRL-1/evidence') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    expect(JSON.parse(String((evidenceCall[1] as RequestInit).body))).toEqual({ kind: 'note', value: 'Q3 access review completed.' });

    // The freshly-appended record renders newest — first in the list.
    const itemsAfter = within(screen.getByRole('list', { name: 'Evidence trail' })).getAllByRole('listitem');
    expect(within(itemsAfter[0]).getByText('Q3 access review completed.')).toBeInTheDocument();
  });

  it('shows no edit or delete affordance anywhere in the evidence trail', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [control()], evidenceByControl: { 'CTRL-1': evidenceOldestFirst } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'CC6.1' }));
    await screen.findByText('Kickoff review scheduled.');

    expect(screen.queryByRole('button', { name: /edit evidence/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /delete evidence/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /remove evidence/i })).not.toBeInTheDocument();
  });
});

describe('control detail — edit', () => {
  it('saves title/description in one PATCH call, with framework/control_ref shown read-only', async () => {
    const f = routedFetch({ items: [control()] });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'CC6.1' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Edit' }));

    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('SOC2 CC6.1');
    expect(within(dialog).queryByLabelText('Framework')).not.toBeInTheDocument();
    expect(within(dialog).queryByLabelText('Control ref')).not.toBeInTheDocument();

    fireEvent.change(within(dialog).getByLabelText('Title'), { target: { value: 'Logical access controls (updated)' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const patchCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/compliance-controls/CTRL-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    const body = JSON.parse(String((patchCall[1] as RequestInit).body));
    expect(body).toEqual({ row_version: 1, title: 'Logical access controls (updated)', description: control().description });
    expect(body.status).toBeUndefined();

    expect((await screen.findAllByText('Logical access controls (updated)')).length).toBeGreaterThan(0);
  });
});
