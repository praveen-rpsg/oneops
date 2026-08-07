import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import createWrapper from '@cloudscape-design/components/test-utils/dom';
import type { IncidentDTO } from '../incidents';
import type { PrioritizedVulnFindingDTO, VulnFindingDTO } from '../vulnerabilities';

// Exercises E-SEC-UI.1's Vulnerabilities screen: list + filter (GET
// /v1/admin/vuln-findings), the prioritized/ranked view (GET
// /v1/admin/vuln-findings/prioritized), legal-only status transitions (PATCH
// .../{id}, internal/domain/vuln_finding.go's vulnFindingTransitions),
// one-click idempotent Remediate (POST .../{id}/remediate) that links
// through to its incident, 409-refetches-with-notice, and the 403-graceful
// permission state.

const finding = (over: Partial<VulnFindingDTO> = {}): VulnFindingDTO => ({
  finding_id: 'VF-1',
  asset_id: 'AST-WEB-1',
  vuln_id: 'CVE-2024-1234',
  title: 'OpenSSL heap overflow',
  severity: 'critical',
  status: 'open',
  first_seen: '2026-08-01T00:00:00Z',
  last_seen: '2026-08-05T00:00:00Z',
  scanner: 'nessus',
  description: 'Heap overflow in libssl affecting outbound TLS.',
  row_version: 1,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-05T00:00:00Z',
  ...over,
});

const remediatedFinding = finding({
  finding_id: 'VF-2',
  asset_id: 'AST-DB-1',
  vuln_id: 'CVE-2024-9999',
  title: 'Postgres privilege escalation',
  severity: 'medium',
  status: 'remediated',
  remediation_incident_id: 'INC-OLD',
});

const linkedIncident: IncidentDTO = {
  incident_id: 'INC-OLD',
  title: 'Remediate CVE-2024-9999 on AST-DB-1',
  description: 'Opened from vulnerability remediation.',
  severity: 'medium',
  status: 'closed',
  source: 'manual',
  asset_id: 'AST-DB-1',
  row_version: 1,
  created_at: '2026-08-02T00:00:00Z',
  updated_at: '2026-08-03T00:00:00Z',
  is_root: true,
};

const freshIncident: IncidentDTO = {
  incident_id: 'INC-NEW',
  title: 'Remediate CVE-2024-1234 on AST-WEB-1',
  description: 'Opened from vulnerability remediation.',
  severity: 'critical',
  status: 'open',
  source: 'manual',
  asset_id: 'AST-WEB-1',
  row_version: 1,
  created_at: '2026-08-06T00:00:00Z',
  updated_at: '2026-08-06T00:00:00Z',
  is_root: true,
};

interface Fixture {
  items?: VulnFindingDTO[];
  prioritized?: PrioritizedVulnFindingDTO[];
  incidents?: Record<string, IncidentDTO>;
  /** GET /admin/vuln-findings answers 403 — not a tenant admin. */
  forbidden?: boolean;
  /** PATCH .../{id} fails 409. */
  transitionConflict?: boolean;
  /** POST .../{id}/remediate fails 409. */
  remediateConflict?: boolean;
}

function routedFetch(fx: Fixture = {}) {
  const items = [...(fx.items ?? [finding()])];
  const incidents: Record<string, IncidentDTO> = { ...(fx.incidents ?? {}) };

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

    if (u.includes('/admin/incidents/') && u.includes('/timeline')) return ok({ items: [] });

    const incidentMatch = u.match(/\/admin\/incidents\/([^/?]+)$/);
    if (incidentMatch) {
      const inc = incidents[incidentMatch[1]];
      if (!inc) return fail(404, { title: 'not found' });
      return ok(inc);
    }

    if (u.includes('/admin/vuln-findings/prioritized')) {
      if (fx.forbidden) return fail(403, { title: 'forbidden', detail: 'missing permission: admin' });
      return ok({ items: fx.prioritized ?? [] });
    }

    const remediateMatch = u.match(/\/admin\/vuln-findings\/([^/?]+)\/remediate$/);
    if (method === 'POST' && remediateMatch) {
      if (fx.remediateConflict) return fail(409, { title: 'conflict', detail: 'vuln finding was modified concurrently; re-read and retry' });
      const i = items.findIndex((it) => it.finding_id === remediateMatch[1]);
      if (i < 0) return fail(404, { title: 'not found' });
      const incident = freshIncident;
      items[i] = { ...items[i], remediation_incident_id: incident.incident_id, row_version: items[i].row_version + 1 };
      incidents[incident.incident_id] = incident;
      return ok({ vuln_finding: items[i], incident });
    }

    const patchMatch = u.match(/\/admin\/vuln-findings\/([^/?]+)$/);
    if (method === 'PATCH' && patchMatch) {
      if (fx.transitionConflict) {
        return fail(409, { title: 'conflict', detail: 'vuln finding was modified concurrently; re-read and retry' });
      }
      const body = JSON.parse(String(init?.body)) as { row_version: number; status: string };
      const i = items.findIndex((it) => it.finding_id === patchMatch[1]);
      if (i < 0) return fail(404, { title: 'not found' });
      items[i] = { ...items[i], status: body.status as VulnFindingDTO['status'], row_version: items[i].row_version + 1 };
      return ok(items[i]);
    }

    if (patchMatch && method === 'GET') {
      const one = items.find((it) => it.finding_id === patchMatch[1]);
      if (!one) return fail(404, { title: 'not found' });
      return ok(one);
    }

    if (u.includes('/admin/vuln-findings')) {
      if (fx.forbidden) return fail(403, { title: 'forbidden', detail: 'missing permission: admin' });
      return ok({ items });
    }

    return ok({});
  });
}

beforeEach(() => {
  window.history.replaceState(null, '', '/security/vulnerabilities');
});
afterEach(() => {
  vi.unstubAllGlobals();
});

describe('vulnerabilities list', () => {
  it('renders findings from a mocked list', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [finding(), remediatedFinding] }));
    renderApp();

    expect(await screen.findByRole('heading', { name: /^Vulnerabilities/ })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'OpenSSL heap overflow' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Postgres privilege escalation' })).toBeInTheDocument();
  });

  it('filters by status', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [finding(), remediatedFinding] }));
    const { container } = renderApp();

    await screen.findByRole('button', { name: 'OpenSSL heap overflow' });

    // Index 0 is the Header's SegmentedControl (view toggle) — it renders its
    // own responsive Select fallback in the DOM regardless of viewport width,
    // which jsdom does not hide (there is no real container-query layout).
    // Index 1/2 are this board's own status/severity filters, in JSX order.
    const statusSelect = createWrapper(container).findAllSelects()[1];
    act(() => statusSelect.openDropdown());
    act(() => statusSelect.selectOptionByValue('remediated'));

    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'OpenSSL heap overflow' })).not.toBeInTheDocument(),
    );
    expect(screen.getByRole('button', { name: 'Postgres privilege escalation' })).toBeInTheDocument();
  });

  it('filters by severity', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [finding(), remediatedFinding] }));
    const { container } = renderApp();

    await screen.findByRole('button', { name: 'OpenSSL heap overflow' });

    const severitySelect = createWrapper(container).findAllSelects()[2];
    act(() => severitySelect.openDropdown());
    act(() => severitySelect.selectOptionByValue('medium'));

    await waitFor(() =>
      expect(screen.queryByRole('button', { name: 'OpenSSL heap overflow' })).not.toBeInTheDocument(),
    );
    expect(screen.getByRole('button', { name: 'Postgres privilege escalation' })).toBeInTheDocument();
  });

  it('renders a Remediation link when remediation_incident_id is set, and an em dash otherwise', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [finding(), remediatedFinding], incidents: { 'INC-OLD': linkedIncident } }));
    renderApp();

    await screen.findByRole('button', { name: 'OpenSSL heap overflow' });

    const unlinkedRow = screen.getByRole('row', { name: /OpenSSL heap overflow/ });
    expect(within(unlinkedRow).getByText('—')).toBeInTheDocument();

    const linkedRow = screen.getByRole('row', { name: /Postgres privilege escalation/ });
    expect(within(linkedRow).getByRole('button', { name: 'INC-OLD' })).toBeInTheDocument();

    fireEvent.click(within(linkedRow).getByRole('button', { name: 'INC-OLD' }));
    expect(await screen.findByRole('heading', { name: 'Incident INC-OLD' })).toBeInTheDocument();
  });

  it('renders the clean permission-needed state on a 403, not a crash', async () => {
    vi.stubGlobal('fetch', routedFetch({ forbidden: true }));
    renderApp();

    expect(
      await screen.findByText('You need tenant-admin permission to manage vulnerability findings.'),
    ).toBeInTheDocument();
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });
});

describe('prioritized view', () => {
  const rows: PrioritizedVulnFindingDTO[] = [
    { finding: finding(), asset_criticality: 'critical', priority_score: 25, priority: 'critical' },
    {
      finding: finding({ finding_id: 'VF-3', vuln_id: 'CVE-2024-0001', title: 'Minor info leak', severity: 'low' }),
      asset_criticality: 'low',
      priority_score: 6,
      priority: 'low',
    },
  ];

  it('toggles to the ranked view and shows priority + asset criticality, highest risk first', async () => {
    vi.stubGlobal('fetch', routedFetch({ prioritized: rows }));
    const { container } = renderApp();

    await screen.findByRole('heading', { name: /^Vulnerabilities/ });
    const segmented = createWrapper(container).findSegmentedControl()!;
    segmented.findSegmentById('prioritized')!.click();

    expect(await screen.findByRole('button', { name: 'OpenSSL heap overflow' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Minor info leak' })).toBeInTheDocument();
    expect(screen.getByText('Priority')).toBeInTheDocument();
    expect(screen.getByText('Asset criticality')).toBeInTheDocument();

    const dataRows = screen.getAllByRole('row').slice(1); // drop the header row
    expect(within(dataRows[0]).getByText('OpenSSL heap overflow')).toBeInTheDocument();
    expect(within(dataRows[1]).getByText('Minor info leak')).toBeInTheDocument();
  });
});

describe('finding detail — status transitions', () => {
  it('offers only legal next statuses (open vs. remediated)', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [finding(), remediatedFinding] }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'OpenSSL heap overflow' }));
    await screen.findByRole('button', { name: 'Mark remediated' });
    expect(screen.getByRole('button', { name: 'Accept risk' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Mark false positive' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Reopen' })).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Postgres privilege escalation' }));
    await waitFor(() => expect(screen.getByRole('button', { name: 'Reopen' })).toBeInTheDocument());
    expect(screen.queryByRole('button', { name: 'Mark remediated' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Accept risk' })).not.toBeInTheDocument();
  });

  it('accepted_risk is a judgment: it requires confirmation, then patches row_version + status and refetches', async () => {
    const f = routedFetch({ items: [finding()] });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'OpenSSL heap overflow' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Accept risk' }));

    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('deliberate decision not to fix');
    expect(f.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'PATCH')).toBe(false);

    fireEvent.click(within(dialog).getByRole('button', { name: 'Accept risk' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());

    const patchCall = f.mock.calls.find(
      ([u, init]) => String(u).endsWith('/admin/vuln-findings/VF-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    )!;
    expect(JSON.parse(String((patchCall[1] as RequestInit).body))).toEqual({ row_version: 1, status: 'accepted_risk' });

    await waitFor(() => expect(screen.getAllByText('Accepted risk').length).toBeGreaterThan(0));
  });

  it('on 409, refetches with a conflict notice and does not blindly retry', async () => {
    const f = routedFetch({ items: [finding()], transitionConflict: true });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'OpenSSL heap overflow' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Mark remediated' }));

    expect(await screen.findByText('Changed since you loaded it')).toBeInTheDocument();

    const patchCalls = f.mock.calls.filter(
      ([u, init]) => String(u).endsWith('/admin/vuln-findings/VF-1') && (init as RequestInit | undefined)?.method === 'PATCH',
    );
    expect(patchCalls.length).toBe(1);
    // Still Open — the conflicting move was never applied.
    expect(screen.getAllByText('Open').length).toBeGreaterThan(0);
  });
});

describe('remediate', () => {
  it('posts row_version, links the returned incident, and refetches; the link opens the incident', async () => {
    const f = routedFetch({ items: [finding()] });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'OpenSSL heap overflow' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Remediate' }));

    const remediateCall = await vi.waitFor(() => {
      const call = f.mock.calls.find(
        ([u, init]) =>
          String(u).endsWith('/admin/vuln-findings/VF-1/remediate') && (init as RequestInit | undefined)?.method === 'POST',
      );
      if (!call) throw new Error('remediate not called yet');
      return call;
    });
    expect(JSON.parse(String((remediateCall[1] as RequestInit).body))).toEqual({ row_version: 1 });

    expect(await screen.findByText('Remediation incident linked')).toBeInTheDocument();
    const links = await screen.findAllByRole('button', { name: 'INC-NEW' });
    expect(links.length).toBeGreaterThan(0);

    fireEvent.click(links[0]);
    expect(await screen.findByRole('heading', { name: 'Incident INC-NEW' })).toBeInTheDocument();
  });

  it('is disabled once the finding is no longer open', async () => {
    vi.stubGlobal('fetch', routedFetch({ items: [remediatedFinding], incidents: { 'INC-OLD': linkedIncident } }));
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'Postgres privilege escalation' }));
    await screen.findByRole('button', { name: 'Reopen' });
    expect(screen.getByRole('button', { name: 'Remediate' })).toBeDisabled();
  });

  it('on 409, refetches with a conflict notice and does not blindly retry', async () => {
    const f = routedFetch({ items: [finding()], remediateConflict: true });
    vi.stubGlobal('fetch', f);
    renderApp();

    fireEvent.click(await screen.findByRole('button', { name: 'OpenSSL heap overflow' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Remediate' }));

    expect(await screen.findByText('Changed since you loaded it')).toBeInTheDocument();
    const remediateCalls = f.mock.calls.filter(
      ([u, init]) =>
        String(u).endsWith('/admin/vuln-findings/VF-1/remediate') && (init as RequestInit | undefined)?.method === 'POST',
    );
    expect(remediateCalls.length).toBe(1);
    expect(screen.queryByText('Remediation incident linked')).not.toBeInTheDocument();
  });
});
