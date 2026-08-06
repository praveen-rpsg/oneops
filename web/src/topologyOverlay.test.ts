import { describe, expect, it } from 'vitest';
import type { AssetHealthReport } from './assetGraph';
import type { IncidentDTO } from './incidents';
import { buildTopologyOverlay, overlayFor } from './topologyOverlay';

const incident = (over: Partial<IncidentDTO> = {}): IncidentDTO => ({
  incident_id: 'INC-1',
  title: 'title',
  description: 'desc',
  severity: 'high',
  status: 'open',
  source: 'alert',
  row_version: 1,
  created_at: '2026-08-06T00:00:00Z',
  updated_at: '2026-08-06T00:00:00Z',
  is_root: true,
  ...over,
});

const emptyHealth: AssetHealthReport = {
  stale_after: '720h',
  stale: { count: 0, samples: [] },
  orphaned_assets: { count: 0, samples: [] },
  orphaned_business_services: { count: 0, samples: [] },
  incomplete: { count: 0, samples: [] },
};

describe('buildTopologyOverlay', () => {
  it('marks a node with an open incident as status incident', () => {
    const overlay = buildTopologyOverlay([incident({ asset_id: 'AST-1', status: 'open' })], null);
    expect(overlayFor(overlay, 'AST-1').status).toBe('incident');
    expect(overlayFor(overlay, 'AST-1').openIncidentCount).toBe(1);
  });

  it('does not count a resolved or closed incident as open', () => {
    const overlay = buildTopologyOverlay(
      [incident({ asset_id: 'AST-1', status: 'resolved' }), incident({ asset_id: 'AST-1', status: 'closed' })],
      null,
    );
    expect(overlayFor(overlay, 'AST-1').status).toBe('none');
    expect(overlayFor(overlay, 'AST-1').openIncidentCount).toBe(0);
  });

  it('ignores an incident with no asset_id', () => {
    const overlay = buildTopologyOverlay([incident({ asset_id: undefined })], null);
    expect(overlay.size).toBe(0);
  });

  it('marks a node in a health category sample as status health', () => {
    const health: AssetHealthReport = {
      ...emptyHealth,
      stale: { count: 1, samples: [{ asset_id: 'AST-2', type: 'server', name: 'old-box' }] },
    };
    const overlay = buildTopologyOverlay([], health);
    const entry = overlayFor(overlay, 'AST-2');
    expect(entry.status).toBe('health');
    expect(entry.healthIssues).toEqual(['Stale']);
  });

  it('an open incident outranks a health issue on the same node', () => {
    const health: AssetHealthReport = {
      ...emptyHealth,
      orphaned_assets: { count: 1, samples: [{ asset_id: 'AST-3', type: 'server', name: 'orphan' }] },
    };
    const overlay = buildTopologyOverlay([incident({ asset_id: 'AST-3', status: 'acknowledged' })], health);
    const entry = overlayFor(overlay, 'AST-3');
    expect(entry.status).toBe('incident');
    expect(entry.healthIssues).toEqual(['Orphaned (no relationships)']);
  });

  it('degrades to incident-only overlay when health is null', () => {
    const overlay = buildTopologyOverlay([incident({ asset_id: 'AST-4', status: 'open' })], null);
    expect(overlayFor(overlay, 'AST-4').status).toBe('incident');
  });

  it('returns a neutral overlay for an asset with no signal at all', () => {
    const overlay = buildTopologyOverlay([], emptyHealth);
    expect(overlayFor(overlay, 'AST-UNKNOWN')).toEqual({ status: 'none', openIncidentCount: 0, healthIssues: [] });
  });
});
