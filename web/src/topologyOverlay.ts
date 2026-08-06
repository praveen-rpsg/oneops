import type { AssetHealthReport } from './assetGraph';
import { isOpenIncidentStatus } from './incidentPresentation';
import type { IncidentDTO } from './incidents';

// Client-side overlay composition for the topology map (E7.3b-2, ADR-NOC-007
// Decision 4 / ADR-NOC-006 §4): the graph endpoint carries no incident or
// health state, so it is merged in here from the existing incidents/health
// endpoints, keyed by asset_id.

export type NodeOverlayStatus = 'incident' | 'health' | 'none';

const HEALTH_CATEGORY_LABEL = {
  stale: 'Stale',
  orphaned_assets: 'Orphaned (no relationships)',
  orphaned_business_services: 'Orphaned business service (no dependency)',
  incomplete: 'Incomplete record',
} as const;

type HealthCategoryKey = keyof typeof HEALTH_CATEGORY_LABEL;

export interface NodeOverlay {
  status: NodeOverlayStatus;
  openIncidentCount: number;
  healthIssues: string[];
}

const NONE_OVERLAY: NodeOverlay = { status: 'none', openIncidentCount: 0, healthIssues: [] };

/**
 * Merges open incidents and CMDB health-report samples onto graph nodes by
 * asset_id. Incident state always wins over a health issue on the same node
 * (an open incident is the more urgent signal); an unlinked incident
 * (`asset_id` absent) contributes nothing, the same "Unlinked" case the
 * incident board already renders explicitly. `health` may be `null` when its
 * fetch failed — the overlay degrades to incident-only rather than blocking
 * the whole map (the on-call board's "degrade a supplementary fetch without
 * blanking the view" precedent, E7.3c).
 */
export function buildTopologyOverlay(
  incidents: IncidentDTO[],
  health: AssetHealthReport | null,
): Map<string, NodeOverlay> {
  const overlay = new Map<string, NodeOverlay>();

  const entryFor = (assetId: string): NodeOverlay => {
    let entry = overlay.get(assetId);
    if (!entry) {
      entry = { status: 'none', openIncidentCount: 0, healthIssues: [] };
      overlay.set(assetId, entry);
    }
    return entry;
  };

  if (health) {
    (Object.keys(HEALTH_CATEGORY_LABEL) as HealthCategoryKey[]).forEach((key) => {
      for (const sample of health[key].samples) {
        const entry = entryFor(sample.asset_id);
        entry.healthIssues.push(HEALTH_CATEGORY_LABEL[key]);
        if (entry.status === 'none') entry.status = 'health';
      }
    });
  }

  for (const inc of incidents) {
    if (!inc.asset_id || !isOpenIncidentStatus(inc.status)) continue;
    const entry = entryFor(inc.asset_id);
    entry.openIncidentCount += 1;
    entry.status = 'incident';
  }

  return overlay;
}

export function overlayFor(overlay: Map<string, NodeOverlay>, assetId: string): NodeOverlay {
  return overlay.get(assetId) ?? NONE_OVERLAY;
}
