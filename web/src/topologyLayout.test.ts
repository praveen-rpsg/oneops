import { describe, expect, it } from 'vitest';
import type { AssetGraphEdge, AssetGraphNode } from './assetGraph';
import { computeTopologyLayout, NODE_HEIGHT, NODE_WIDTH } from './topologyLayout';

function node(id: string, over: Partial<AssetGraphNode> = {}): AssetGraphNode {
  return {
    asset_id: id,
    name: id,
    type: 'service',
    status: 'active',
    environment: 'production',
    criticality: 'medium',
    ...over,
  };
}

function edge(from: string, to: string, type: AssetGraphEdge['type'] = 'depends_on'): AssetGraphEdge {
  return { from_asset_id: from, to_asset_id: to, type };
}

describe('computeTopologyLayout', () => {
  it('returns an empty layout for an empty graph', () => {
    const layout = computeTopologyLayout([], []);
    expect(layout.nodes).toHaveLength(0);
    expect(layout.edges).toHaveLength(0);
    expect(layout.width).toBe(0);
    expect(layout.height).toBe(0);
  });

  it('lays out a single, edgeless node', () => {
    const layout = computeTopologyLayout([node('A')], []);
    expect(layout.nodes).toHaveLength(1);
    expect(layout.nodes[0].rank).toBe(0);
    expect(layout.edges).toHaveLength(0);
    expect(layout.width).toBeGreaterThan(NODE_WIDTH);
    expect(layout.height).toBeGreaterThan(NODE_HEIGHT);
  });

  it('ranks a dependent above its dependency: WEB -> API -> DB', () => {
    const nodes = [node('WEB'), node('API'), node('DB')];
    const edges = [edge('WEB', 'API'), edge('API', 'DB')];
    const layout = computeTopologyLayout(nodes, edges);

    const rankOf = (id: string) => layout.nodes.find((n) => n.asset.asset_id === id)!.rank;
    expect(rankOf('DB')).toBe(0);
    expect(rankOf('API')).toBe(1);
    expect(rankOf('WEB')).toBe(2);

    // A dependent (WEB) is drawn above its dependency (API is above DB, WEB above API).
    const yOf = (id: string) => layout.nodes.find((n) => n.asset.asset_id === id)!.y;
    expect(yOf('WEB')).toBeLessThan(yOf('API'));
    expect(yOf('API')).toBeLessThan(yOf('DB'));

    expect(layout.edges.every((e) => !e.isBackEdge)).toBe(true);
  });

  it('places a disconnected node in its own layer without touching the rest of the graph', () => {
    const nodes = [node('WEB'), node('API'), node('ISOLATED')];
    const edges = [edge('WEB', 'API')];
    const layout = computeTopologyLayout(nodes, edges);

    expect(layout.nodes).toHaveLength(3);
    const isolated = layout.nodes.find((n) => n.asset.asset_id === 'ISOLATED')!;
    expect(isolated.rank).toBe(0);
  });

  it('is cycle-tolerant and terminates: A -> B -> C -> A never hangs, and the closing edge is a marked back edge', () => {
    const nodes = [node('A'), node('B'), node('C')];
    const edges = [edge('A', 'B'), edge('B', 'C'), edge('C', 'A')];

    const start = Date.now();
    const layout = computeTopologyLayout(nodes, edges);
    expect(Date.now() - start).toBeLessThan(1000);

    expect(layout.nodes).toHaveLength(3);
    expect(layout.edges).toHaveLength(3);

    const backEdges = layout.edges.filter((e) => e.isBackEdge);
    expect(backEdges).toHaveLength(1);
    expect(backEdges[0].edge).toEqual(edge('C', 'A'));

    // Every rank is finite and non-negative — no node was left unranked or corrupted by the cycle.
    for (const n of layout.nodes) {
      expect(Number.isFinite(n.rank)).toBe(true);
      expect(n.rank).toBeGreaterThanOrEqual(0);
    }
  });

  it('handles a self-loop without hanging or losing the node', () => {
    const nodes = [node('A')];
    const edges = [edge('A', 'A')];
    const layout = computeTopologyLayout(nodes, edges);

    expect(layout.nodes).toHaveLength(1);
    expect(layout.edges).toHaveLength(1);
    expect(layout.edges[0].isBackEdge).toBe(true);
    expect(layout.nodes[0].rank).toBe(0);
  });

  it('produces an identical layout for the same input on repeated calls (determinism)', () => {
    const nodes = [node('C'), node('A'), node('B')];
    const edges = [edge('A', 'B'), edge('B', 'C'), edge('C', 'A'), edge('A', 'C')];

    const first = computeTopologyLayout(nodes, edges);
    const second = computeTopologyLayout([...nodes], [...edges]);

    expect(second).toEqual(first);
  });

  it('drops edges that reference an asset_id outside the given node set', () => {
    const nodes = [node('A'), node('B')];
    const edges = [edge('A', 'B'), edge('A', 'MISSING'), edge('MISSING', 'B')];
    const layout = computeTopologyLayout(nodes, edges);

    expect(layout.edges).toHaveLength(1);
    expect(layout.edges[0].edge).toEqual(edge('A', 'B'));
  });
});
