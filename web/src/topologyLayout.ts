import type { AssetGraphEdge, AssetGraphNode } from './assetGraph';

// Deterministic layered layout for the hand-rolled SVG topology map
// (E7.3b-2, ADR-NOC-007). No viz library — this is the whole layout engine.

export const NODE_WIDTH = 180;
export const NODE_HEIGHT = 56;

const LAYER_GAP_Y = 90;
const NODE_GAP_X = 32;
const MARGIN = 40;

export interface LayoutNode {
  asset: AssetGraphNode;
  /** Dependency-depth rank: 0 is a leaf dependency, higher ranks depend on lower ones. */
  rank: number;
  /** Top-left corner in SVG user units; the node box is NODE_WIDTH x NODE_HEIGHT. */
  x: number;
  y: number;
}

export interface LayoutEdge {
  edge: AssetGraphEdge;
  from: LayoutNode;
  to: LayoutNode;
  /** True when this edge closes a cycle — still drawn, never used for ranking. */
  isBackEdge: boolean;
}

export interface TopologyLayout {
  nodes: LayoutNode[];
  edges: LayoutEdge[];
  width: number;
  height: number;
}

const EMPTY_LAYOUT: TopologyLayout = { nodes: [], edges: [], width: 0, height: 0 };

/**
 * Ranks every node by dependency depth and lays it out in top-down layers:
 * edges run dependent -> dependency (`from_asset_id` depends on
 * `to_asset_id`, matching AssetGraphEdge/ADR-NOC-006), so a node nothing
 * depends on sits in a top layer and its dependencies sit below it. Rank is
 * computed as a DFS longest path over the dependency edges: a node with no
 * (forward) children ranks 0, otherwise it ranks one above the highest
 * ranked child.
 *
 * Cycle-tolerant by construction: a classic white/gray/black DFS colouring
 * marks a node gray while it is on the current recursion stack. An edge to a
 * gray node is a back edge — it closes a cycle back onto an ancestor still
 * being visited — and is never recursed into (recursing into it is exactly
 * what would infinite-loop). The edge is kept and returned with
 * `isBackEdge: true` so it still renders, but it never contributes to any
 * rank. Because every node is visited at most once (white -> gray -> black,
 * never gray -> gray), the whole computation is O(nodes + edges) regardless
 * of how many cycles the input graph contains.
 *
 * Deterministic: node ids and edges are sorted before any traversal, so an
 * identical `{nodes, edges}` input always produces an identical layout,
 * including which layer position each node lands in and which edges are
 * classified as back edges.
 */
export function computeTopologyLayout(nodes: AssetGraphNode[], edges: AssetGraphEdge[]): TopologyLayout {
  if (nodes.length === 0) return EMPTY_LAYOUT;

  const byId = new Map(nodes.map((n) => [n.asset_id, n]));
  const sortedIds = [...byId.keys()].sort();

  // Edges referencing an asset_id absent from this response's node list
  // cannot be laid out or drawn meaningfully; drop them rather than guessing.
  const usableEdges = edges.filter((e) => byId.has(e.from_asset_id) && byId.has(e.to_asset_id));
  const sortedEdges = [...usableEdges].sort(
    (a, b) =>
      a.from_asset_id.localeCompare(b.from_asset_id) ||
      a.to_asset_id.localeCompare(b.to_asset_id) ||
      a.type.localeCompare(b.type),
  );

  const adjacency = new Map<string, string[]>();
  for (const id of sortedIds) adjacency.set(id, []);
  for (const e of sortedEdges) adjacency.get(e.from_asset_id)!.push(e.to_asset_id);

  const WHITE = 0;
  const GRAY = 1;
  const BLACK = 2;
  const color = new Map<string, number>();
  const rank = new Map<string, number>();
  const backEdgeDirections = new Set<string>(); // "from to" pairs found to be back edges

  function dfs(id: string): number {
    color.set(id, GRAY);
    let maxChildRank = -1;
    for (const child of adjacency.get(id) ?? []) {
      const childColor = color.get(child) ?? WHITE;
      if (childColor === GRAY) {
        // Back edge: child is an ancestor still on the recursion stack.
        // Recording and skipping — never recursing — is what keeps this
        // termination-guaranteed for any cycle, including a self-loop
        // (from === to, which is gray for itself the instant it starts).
        backEdgeDirections.add(`${id} ${child}`);
        continue;
      }
      const childRank = childColor === BLACK ? rank.get(child)! : dfs(child);
      if (childRank > maxChildRank) maxChildRank = childRank;
    }
    color.set(id, BLACK);
    const r = maxChildRank + 1;
    rank.set(id, r);
    return r;
  }

  for (const id of sortedIds) {
    if ((color.get(id) ?? WHITE) === WHITE) dfs(id);
  }

  const maxRank = Math.max(...sortedIds.map((id) => rank.get(id)!));

  // Layer index 0 is the top of the drawing (highest rank — nothing depends
  // on it further) descending to the bottom (rank 0 — leaf dependencies).
  const layers: string[][] = Array.from({ length: maxRank + 1 }, () => []);
  for (const id of sortedIds) {
    layers[maxRank - rank.get(id)!].push(id); // sortedIds is already alphabetical: stable, deterministic order within a layer
  }

  const layoutById = new Map<string, LayoutNode>();
  layers.forEach((layerIds, layerIndex) => {
    layerIds.forEach((id, posInLayer) => {
      layoutById.set(id, {
        asset: byId.get(id)!,
        rank: rank.get(id)!,
        x: MARGIN + posInLayer * (NODE_WIDTH + NODE_GAP_X),
        y: MARGIN + layerIndex * (NODE_HEIGHT + LAYER_GAP_Y),
      });
    });
  });

  const maxLayerWidth = Math.max(...layers.map((l) => l.length));
  const width = MARGIN * 2 + maxLayerWidth * NODE_WIDTH + Math.max(0, maxLayerWidth - 1) * NODE_GAP_X;
  const height = MARGIN * 2 + layers.length * NODE_HEIGHT + Math.max(0, layers.length - 1) * LAYER_GAP_Y;

  const layoutEdges: LayoutEdge[] = sortedEdges.map((edge) => ({
    edge,
    from: layoutById.get(edge.from_asset_id)!,
    to: layoutById.get(edge.to_asset_id)!,
    isBackEdge: backEdgeDirections.has(`${edge.from_asset_id} ${edge.to_asset_id}`),
  }));

  return {
    nodes: sortedIds.map((id) => layoutById.get(id)!),
    edges: layoutEdges,
    width,
    height,
  };
}
