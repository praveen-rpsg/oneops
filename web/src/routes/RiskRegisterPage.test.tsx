import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { RiskDTO, RiskRegisterEntryDTO } from '../risks';

// Exercises E-SEC-UI.3's Risk register screen: list + filter (GET
// /v1/admin/risks), "Register risk" (POST .../risks), full-field Edit and
// legal-only status transitions (PATCH .../{id}, internal/domain/risk.go's
// riskTransitions), the risk-matrix "Register" projection (GET
// .../risks/register, ranked highest score first), 409-refetches-with-
// notice, and the 403-graceful permission state.

const risk = (over: Partial<RiskDTO> = {}): RiskDTO => ({
  risk_id: 'RISK-1',
  title: 'Single vendor dependency',
  description: 'All production traffic depends on one upstream CDN provider.',
  category: 'operational',
  likelihood: 'possible',
  impact: 'major',
  status: 'open',
  row_version: 1,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-05T00:00:00Z',
  ...over,
});

const mitigatingRisk = risk({
  risk_id: 'RISK-2',
  title: 'Unpatched legacy OS',
  category: 'security',
  likelihood: 'likely',
  impact: 'severe',
  status: 'mitigating',
  treatment: 'mitigate',
  asset_id: 'AST-LEGACY-1',
});

interface Fixture {
  items?: RiskDTO[];
  registerItems?: RiskRegisterEntryDTO[];
  /** GET /admin/risks answers 403 — not a tenant admin. */
  forbidden?: boolean;
  /** PATCH .../{id} fails 409. */
  transitionConflict?: boolean;
}

function routedFetch(fx: Fixture = {}) {
  const items = [...(fx.items ?? [risk()])];

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

    if (u.includes('/admin/risks/register')) {
      if (fx.forbidden) return fail(403, { title: 'forbidden', detail: 'missing permission: admin' });
      return ok({ items: fx.registerItems ?? [] });
    }

    if (method === 'POST' && /\/admin\/risks$/.test(u)) {
      const body = JSON.parse(String(init?.body)) as Partial<RiskDTO>;
      const created = risk({
        risk_id: 'RISK-NEW',
        title: body.title ?? '',
        description: body.description ?? '',
        category: body.category ?? '',
        likelihood: body.likelihood ?? 'possible',
        impact: body.impact ?? 'moderate',
        status: 'open',
        treatment: body.treatment,
        asset_id: body.asset_id,
      });
      items.push(created);
      return ok(created, 201);
    }

    const patchMatch = u.match(/\/admin\/risks\/([^/?]+)$/);
    if (method === 'PATCH' && patchMatch) {
      if (fx.transitionConflict) {
        return fail(409, { title: 'conflict', detail: 'risk was modified concurrently; re-read and retry' });
      }
      const body = JSON.parse(String(init?.body)) as Record<string, unknown>;
      const i = items.findIndex((it) => it.risk_id === patchMatch[1]);
      if (i < 0) return fail(404, { title: 'not found' });
      const { row_version: _rv, ...rest } = body;
      items[i] = { ...items[i], ...rest, row_version: items[i].row_version + 1 } as RiskDTO;
      return ok(items[i]);
    }

    if (patchMatch && method === 'GET') {
      const one = items.find((it) => it.risk_id === patchMatch[1]);
      if (!one) return fail(404, { title: 'not found' });
      return ok(one);
    }

    if (u.includes('/admin/risks')) {
      if (fx.forbidden) return fail(403, { title: 'forbidden', detail: 'missing permission: admin' });
      return ok({ items });
    }

    return ok({});
  });
}

beforeEach(() => {
  window.history.replaceState(null, '', '/security/risks');
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe('risk register board', () => {
  it('renders risks from a mocked list', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [risk(), mitigatingRisk] }));
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Risk register/ })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Single vendor dependency' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Unpatched legacy OS' })).toBeInTheDocument();
  });

  it('filters by status', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [risk(), mitigatingRisk] }));
    const { container } = renderApp();

    await screen.findByRole('button', { name: 'Single vendor dependency' });

    const statusSelect = createWrapper(container).findAllSelects()[1];
    act(() => statusSelect.openDropdown());
    act(() => statusSelect.selectOptionByValue('mitigating'));

    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'Single vendor dependency' })).not.toBeInTheDocument(),
    );
    expect(screen.getByRole('button', { name: 'Unpatched legacy OS' })).toBeInTheDocument();
  });

  it('renders the clean permission-needed state on a 403, not a crash', async () => {
    vi.stubGlobal('fetch', routedFetch({ forbidden: true }));
    renderApp();

    expect(
      await screen.findByText('You need tenant-admin permission to manage the risk register.'),
    ).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });
});

describe('register view', () => {
  const rows: RiskRegisterEntryDTO[] = [
    { risk: mitigatingRisk, score: 20, band: 'critical' },
    { risk: risk(), score: 12, band: 'medium' },
  ];

  it('toggles to the ranked view and shows score + severity band, highest risk first', async () => {
    vi.stubGlobal('fetch', routedFetch({ registerItems: rows }));
    const { container } = renderApp();

    await screen.findByRole('heading', { name: /^Risk register/ });
    const segmented = createWrapper(container).findSegmentedControl()!;
    segmented.findSegmentById('register')!.click();

    expect(await screen.findByRole('button', { name: 'Unpatched legacy OS' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Single vendor dependency' })).toBeInTheDocument();
    expect(screen.getByText('Score')).toBeInTheDocument();
    expect(screen.getByText('Severity band')).toBeInTheDocument();

    const dataRows = screen.getAllByRole('row').slice(1); // drop the header row
    expect(within(dataRows[0]).getByText('Unpatched legacy OS')).toBeInTheDocument();
    expect(within(dataRows[1]).getByText('Single vendor dependency')).toBeInTheDocument();
  });
});

describe('create risk', () => {
  it('is disabled until a title is filled', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [risk()] }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Register risk' }));
    const dialog = await screen.findByRole('dialog');

    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
  });

  it('constrains likelihood/impact/treatment to the real backend enum values', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [risk()] }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Register risk' }));
    const dialog = await screen.findByRole('dialog');

    const likelihoodSelect = createWrapper(dialog).findAllSelects()[0];
    act(() => likelihoodSelect.openDropdown());
    const likelihoodValues = likelihoodSelect
      .findDropdown()
      .findOptions()
      .map((o) => o.getElement().textContent);
    expect(likelihoodValues.join(' ')).toMatch(/Rare.*Unlikely.*Possible.*Likely.*Almost certain/s);

    const impactSelect = createWrapper(dialog).findAllSelects()[1];
    act(() => impactSelect.openDropdown());
    const impactValues = impactSelect
      .findDropdown()
      .findOptions()
      .map((o) => o.getElement().textContent);
    expect(impactValues.join(' ')).toMatch(/Negligible.*Minor.*Moderate.*Major.*Severe/s);

    const treatmentSelect = createWrapper(dialog).findAllSelects()[2];
    act(() => treatmentSelect.openDropdown());
    const treatmentValues = treatmentSelect
      .findDropdown()
      .findOptions()
      .map((o) => o.getElement().textContent);
    expect(treatmentValues.join(' ')).toMatch(/Not yet decided.*Mitigate.*Accept.*Transfer.*Avoid/s);
  });

  it('posts the exact create body and opens the new risk', async () => {
    const f = routedFetch({ items: [risk()] });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Register risk' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Title'), { target: { value: 'New third-party API dependency' } });
    fireEvent.change(within(dialog).getByLabelText('Category'), { target: { value: 'operational' } });

    const impactSelect = createWrapper(dialog).findAllSelects()[1];
    act(() => impactSelect.openDropdown());
    act(() => impactSelect.selectOptionByValue('severe'));

    fireEvent.click(within(dialog).getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const createCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/risks') && (init as RequestInit | undefined)?.method === 'POST',
    )!;
    expect(JSON.parse(String((createCall[1] as RequestInit).body))).toEqual({
      title: 'New third-party API dependency',
      category: 'operational',
      likelihood: 'possible',
      impact: 'severe',
    });

    expect(await screen.findByRole('heading', { name: 'New third-party API dependency' })).toBeInTheDocument();
  });

  it('rejects an illegal category before ever sending a request', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [risk()] }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Register risk' }));
    const dialog = await screen.findByRole('dialog');

    fireEvent.change(within(dialog).getByLabelText('Title'), { target: { value: 'Bad category test' } });
    fireEvent.change(within(dialog).getByLabelText('Category'), { target: { value: 'Not Valid!' } });

    expect(await within(dialog).findByText(/Must be lower-case snake_case/)).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Create' })).toBeDisabled();
  });
});

describe('risk detail — status transitions', () => {
  it('offers only legal next statuses (open vs. mitigating)', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [risk(), mitigatingRisk] }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Single vendor dependency' }));
    await screen.findByRole('button', { name: 'Start mitigating' });
    expect(screen.getByRole('button', { name: 'Accept risk' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Close' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reopen' })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Unpatched legacy OS' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Reopen' })).toBeInTheDocument());
    expect(screen.getByRole('button', { name: 'Close' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Start mitigating' })).not.toBeInTheDocument();
  });

  it('accepted is a judgment: it requires confirmation, then patches row_version + status and refetches', async () => {
    const f = routedFetch({ items: [risk()] });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Single vendor dependency' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Accept risk' }));

    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('deliberate decision to accept');
    expect(f.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Accept risk' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const patchCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/risks/RISK-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    expect(JSON.parse(String((patchCall[1] as RequestInit).body))).toEqual({ row_version: 1, status: 'accepted' });

    await waitFor(() => expect(screen.getAllByText('Accepted').length).toBeGreaterThan(0));
  });

  it('on 409, refetches with a conflict notice and does not blindly retry', async () => {
    const f = routedFetch({ items: [risk()], transitionConflict: true });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Single vendor dependency' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Start mitigating' }));

    expect(await screen.findByText('Changed since you loaded it')).toBeInTheDocument();

    const patchCalls = f.mock.calls.filter(
      ([u, init]) => String(u).endsWith('/admin/risks/RISK-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    );
    expect(patchCalls.length).toBe(1);
    // Still Open — the conflicting move was never applied.
    expect(screen.getAllByText('Open').length).toBeGreaterThan(0);
  });
});

describe('risk detail — edit', () => {
  it('saves every field in one PATCH call', async () => {
    const f = routedFetch({ items: [risk()] });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Single vendor dependency' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Edit' }));

    const dialog = await screen.findByRole('dialog');
    fireEvent.change(within(dialog).getByLabelText('Title'), { target: { value: 'Single vendor dependency (updated)' } });

    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const patchCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/risks/RISK-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    const body = JSON.parse(String((patchCall[1] as RequestInit).body));
    expect(body.title).toBe('Single vendor dependency (updated)');
    expect(body.row_version).toBe(1);
    expect(body.status).toBeUndefined();

    expect((await screen.findAllByText('Single vendor dependency (updated)')).length).toBeGreaterThan(0);
  });
});
