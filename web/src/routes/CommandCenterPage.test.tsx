import { screen } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { NOCOverview } from '../noc';
import type { PrioritizedVulnFindingDTO } from '../vulnerabilities';
import type { RiskRegisterEntryDTO } from '../risks';
import type { ComplianceControlDTO } from '../complianceControls';

// Exercises E-EXEC.1's Command Center: a projection composed entirely over
// GET /v1/admin/noc/overview, /vuln-findings/prioritized, /risks/register
// and /compliance-controls — each card fetches, renders, and degrades
// independently of the other three.

const overview = (over: Partial<NOCOverview> = {}): NOCOverview => ({
  incidents: {
    open_total: 3,
    by_status: { open: 1, acknowledged: 1, investigating: 1 },
    by_severity: { critical: 1, high: 1, medium: 1, low: 0 },
    grouped: { root_count: 2, collateral_count: 1 },
  },
  alerts: { firing_total: 2, by_severity: { critical: 1, warning: 1, info: 0 } },
  assets: { stale: 0, orphaned_assets: 0, orphaned_business_services: 0, incomplete: 0 },
  on_call: [
    { schedule_id: 'SCH1', schedule_name: 'Primary on-call', user_id: 'U1', display_name: 'Ada Lovelace' },
  ],
  escalations: { active_total: 1 },
  generated_at: '2026-08-10T12:00:00Z',
  ...over,
});

const vulnFinding = (over: Partial<PrioritizedVulnFindingDTO['finding']> = {}) => ({
  finding_id: 'VF-1',
  asset_id: 'AST-WEB-1',
  vuln_id: 'CVE-2024-1234',
  title: 'OpenSSL heap overflow',
  severity: 'critical' as const,
  status: 'open' as const,
  first_seen: '2026-08-01T00:00:00Z',
  last_seen: '2026-08-05T00:00:00Z',
  scanner: 'nessus',
  description: 'Heap overflow in libssl.',
  row_version: 1,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-05T00:00:00Z',
  ...over,
});

const prioritizedFindings: PrioritizedVulnFindingDTO[] = [
  { finding: vulnFinding(), asset_criticality: 'critical', priority_score: 100, priority: 'critical' },
  {
    finding: vulnFinding({ finding_id: 'VF-2', asset_id: 'AST-DB-1', vuln_id: 'CVE-2024-5678', severity: 'high' }),
    asset_criticality: 'high',
    priority_score: 80,
    priority: 'high',
  },
];

const riskEntries: RiskRegisterEntryDTO[] = [
  {
    risk: {
      risk_id: 'RISK-1',
      title: 'Unpatched edge routers',
      description: '',
      category: 'operational',
      likelihood: 'likely',
      impact: 'severe',
      status: 'open',
      row_version: 1,
      created_at: '2026-08-01T00:00:00Z',
      updated_at: '2026-08-01T00:00:00Z',
    },
    score: 20,
    band: 'critical',
  },
  {
    risk: {
      risk_id: 'RISK-2',
      title: 'Single vendor lock-in',
      description: '',
      category: 'operational',
      likelihood: 'possible',
      impact: 'major',
      status: 'mitigating',
      row_version: 1,
      created_at: '2026-08-01T00:00:00Z',
      updated_at: '2026-08-01T00:00:00Z',
    },
    score: 12,
    band: 'high',
  },
];

const complianceControls: ComplianceControlDTO[] = [
  {
    control_id: 'CC-1',
    framework: 'SOC2',
    control_ref: 'CC6.1',
    title: 'Access control policy',
    description: '',
    status: 'implemented',
    row_version: 1,
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T00:00:00Z',
  },
  {
    control_id: 'CC-2',
    framework: 'SOC2',
    control_ref: 'CC7.1',
    title: 'Vulnerability management',
    description: '',
    status: 'in_progress',
    row_version: 1,
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T00:00:00Z',
  },
  {
    control_id: 'CC-3',
    framework: 'ISO27001',
    control_ref: 'A.5.1',
    title: 'Information security policy',
    description: '',
    status: 'not_implemented',
    row_version: 1,
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T00:00:00Z',
  },
];

/** The console fetches /auth/config before any data request; everything else is routed by URL fragment. */
function routedFetch(over: Record<string, unknown> = {}) {
  return vi.fn().mockImplementation(async (url: string) => {
    if (String(url).includes('/auth/config')) {
      return { ok: true, status: 200, json: async () => ({ auth_enabled: false }) };
    }
    for (const [frag, body] of Object.entries(over)) {
      if (String(url).includes(frag)) {
        if (body === 'FORBIDDEN') {
          return {
            ok: false,
            status: 403,
            json: async () => ({ title: 'forbidden', status: 403, detail: 'missing permission' }),
          };
        }
        if (body === 'ERROR') {
          return {
            ok: false,
            status: 500,
            json: async () => ({ title: 'internal error', status: 500, detail: 'boom' }),
          };
        }
        return { ok: true, status: 200, json: async () => body };
      }
    }
    return { ok: true, status: 200, json: async () => ({ items: [] }) };
  });
}

const ALL_ENDPOINTS = {
  '/v1/admin/noc/overview': overview(),
  '/v1/admin/vuln-findings/prioritized': { items: prioritizedFindings },
  '/v1/admin/risks/register': { items: riskEntries },
  '/v1/admin/compliance-controls': { items: complianceControls },
};

beforeEach(() => window.history.replaceState(null, '', '/command-center'));
afterEach(() => vi.unstubAllGlobals());

describe('Command Center page', () => {
  it('renders every card from its own composed endpoint, with correctly ranked top-N lists', async () => {
    vi.stubGlobal('fetch', routedFetch(ALL_ENDPOINTS));
    renderApp();

    expect(await screen.findByRole('heading', { name: 'Command Center' })).toBeInTheDocument();

    // Operations — from GET /v1/admin/noc/overview, reusing the on-call list already on it.
    expect(await screen.findByRole('heading', { name: 'Operations' })).toBeInTheDocument();
    expect(screen.getByText('Open incidents')).toBeInTheDocument();
    expect(screen.getByText('3')).toBeInTheDocument();
    expect(screen.getByText('All clear')).toBeInTheDocument();
    expect(screen.getByText(/Ada Lovelace/)).toBeInTheDocument();

    // Vulnerabilities — top 5 ranked by priority (critical before high).
    expect(screen.getByRole('heading', { name: 'Vulnerabilities' })).toBeInTheDocument();
    expect(screen.getByText('Open findings')).toBeInTheDocument();
    expect(screen.getByText('All 2 open findings')).toBeInTheDocument();
    const vulnRows = screen.getAllByText(/CVE-2024-/);
    expect(vulnRows).toHaveLength(2);
    expect(vulnRows[0]).toHaveTextContent('CVE-2024-1234'); // critical ranks first
    expect(vulnRows[1]).toHaveTextContent('CVE-2024-5678'); // high ranks second

    // Risk — top 5 ranked by score (critical band before high band).
    expect(screen.getByRole('heading', { name: 'Risk' })).toBeInTheDocument();
    expect(screen.getByText('Open risks')).toBeInTheDocument();
    expect(screen.getByText('All 2 open risks')).toBeInTheDocument();
    expect(screen.getByText(/Unpatched edge routers/)).toBeInTheDocument();
    expect(screen.getByText(/Single vendor lock-in/)).toBeInTheDocument();

    // Compliance — counted by status.
    expect(screen.getByRole('heading', { name: 'Compliance' })).toBeInTheDocument();
    expect(screen.getByText('Implemented')).toBeInTheDocument();
    expect(screen.getByText('Not implemented')).toBeInTheDocument();

    // Every card links out to its own detail screen.
    const viewAllLinks = screen.getAllByRole('link', { name: 'View all →' });
    expect(viewAllLinks).toHaveLength(4);
  });

  it('degrades a single card on 403 without blanking the rest of the page', async () => {
    vi.stubGlobal(
      'fetch',
      routedFetch({ ...ALL_ENDPOINTS, '/v1/admin/vuln-findings/prioritized': 'FORBIDDEN' }),
    );
    renderApp();

    expect(await screen.findByRole('heading', { name: 'Command Center' })).toBeInTheDocument();
    expect(await screen.findByText('Vulnerability posture unavailable')).toBeInTheDocument();

    // The other three cards still render their own data normally.
    expect(screen.getByRole('heading', { name: 'Operations' })).toBeInTheDocument();
    expect(screen.getByText('Open incidents')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Risk' })).toBeInTheDocument();
    expect(screen.getByText(/Unpatched edge routers/)).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Compliance' })).toBeInTheDocument();
    expect(screen.getByText('Implemented')).toBeInTheDocument();
  });

  it('degrades a single card on a transient server error without blanking the rest of the page', async () => {
    vi.stubGlobal('fetch', routedFetch({ ...ALL_ENDPOINTS, '/v1/admin/risks/register': 'ERROR' }));
    renderApp();

    expect(await screen.findByRole('heading', { name: 'Command Center' })).toBeInTheDocument();
    expect(await screen.findByText('Risk posture unavailable')).toBeInTheDocument();

    expect(screen.getByRole('heading', { name: 'Operations' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Vulnerabilities' })).toBeInTheDocument();
    expect(screen.getByText(/CVE-2024-1234/)).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Compliance' })).toBeInTheDocument();
  });
});
