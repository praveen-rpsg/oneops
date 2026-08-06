import { getJSON } from './api';

// Typed contract for GET /v1/admin/assets/graph and GET /v1/admin/assets/health.
// Mirrors internal/httpapi/handlers_assets.go's assetGraphResponse /
// assetHealthReportDTO exactly — field names and shape, not a paraphrase
// (ADR-NOC-006, ADR-NOC-007). Kept hand-written, like incidents.ts/noc.ts,
// until a second consumer of the generated spec appears.

export const ASSET_STATUSES = ['planned', 'active', 'maintenance', 'retired'] as const;
export type AssetStatus = (typeof ASSET_STATUSES)[number];

export const ASSET_ENVIRONMENTS = ['production', 'staging', 'development', 'unknown'] as const;
export type AssetEnvironment = (typeof ASSET_ENVIRONMENTS)[number];

export const ASSET_CRITICALITIES = ['critical', 'high', 'medium', 'low', 'unknown'] as const;
export type AssetCriticality = (typeof ASSET_CRITICALITIES)[number];

export interface AssetGraphNode {
  asset_id: string;
  name: string;
  type: string;
  status: AssetStatus;
  environment: AssetEnvironment;
  criticality: AssetCriticality;
}

export const ASSET_RELATIONSHIP_TYPES = ['depends_on', 'runs_on', 'connected_to', 'member_of'] as const;
export type AssetRelationshipType = (typeof ASSET_RELATIONSHIP_TYPES)[number];

export interface AssetGraphEdge {
  from_asset_id: string;
  to_asset_id: string;
  type: AssetRelationshipType;
}

/** ADR-NOC-006: bounded at 2000 nodes / 5000 edges; `truncated` signals either cap was hit. */
export interface AssetGraph {
  nodes: AssetGraphNode[];
  edges: AssetGraphEdge[];
  truncated: boolean;
}

export function getAssetGraph(signal?: AbortSignal): Promise<AssetGraph> {
  return getJSON<AssetGraph>('/v1/admin/assets/graph', signal);
}

/** One Configuration Item inside a bounded health-report category sample (E1.5). */
export interface AssetHealthSample {
  asset_id: string;
  type: string;
  name: string;
}

/** `count` is the true, uncapped total; `samples` is bounded — never assume every affected asset_id appears here. */
export interface AssetHealthCategory {
  count: number;
  samples: AssetHealthSample[];
}

export interface AssetHealthReport {
  stale_after: string;
  stale: AssetHealthCategory;
  orphaned_assets: AssetHealthCategory;
  orphaned_business_services: AssetHealthCategory;
  incomplete: AssetHealthCategory;
}

export function getAssetHealth(signal?: AbortSignal): Promise<AssetHealthReport> {
  return getJSON<AssetHealthReport>('/v1/admin/assets/health', signal);
}
