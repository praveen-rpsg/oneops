import { useEffect, useMemo, useState } from 'react';
import { useOutletContext } from 'react-router-dom';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Container from '@cloudscape-design/components/container';
import Header from '@cloudscape-design/components/header';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';
import type { ShellSplitPanelContext } from '../Shell';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { getAssetGraph, getAssetHealth } from '../assetGraph';
import type { AssetGraph, AssetGraphNode, AssetHealthReport } from '../assetGraph';
import { TopologyGraph } from '../components/TopologyGraph';
import { TopologyNodeDetail } from '../components/TopologyNodeDetail';
import { ErrorState } from '../components/States';
import { listIncidents } from '../incidents';
import type { IncidentDTO } from '../incidents';
import { computeTopologyLayout } from '../topologyLayout';
import { buildTopologyOverlay, overlayFor } from '../topologyOverlay';
import { topologyPalette, useTopologyMode } from '../topologyPresentation';

function TopologyEmpty() {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No assets in the CMDB yet</b>
        <Box variant="p" color="text-body-secondary">
          Register assets and relationships (<code>POST /v1/admin/assets</code>,{' '}
          <code>POST /v1/admin/assets/relationships</code>) to see the dependency graph.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

/**
 * The CMDB topology map: the tenant's whole asset dependency graph
 * (`GET /v1/admin/assets/graph`, ADR-NOC-006) rendered as a hand-rolled,
 * layered SVG, with an incident/health overlay composed client-side from the
 * existing incident and asset-health endpoints (ADR-NOC-007).
 */
export function TopologyPage() {
  const { openSplitPanel } = useOutletContext<ShellSplitPanelContext>();
  const mode = useTopologyMode();
  const palette = useMemo(() => topologyPalette(mode), [mode]);

  const [graph, setGraph] = useState<AssetGraph | null>(null);
  const [incidents, setIncidents] = useState<IncidentDTO[]>([]);
  const [health, setHealth] = useState<AssetHealthReport | null>(null);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [selectedAssetId, setSelectedAssetId] = useState<string | undefined>(undefined);
  const [reloads, setReloads] = useState(0);

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);

    getAssetGraph(ctrl.signal)
      .then((g) => setGraph(g))
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setGraph(null);
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load the asset graph', status: 0, detail: String(err) },
        );
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });

    // Overlay sources are supplementary: a failure here degrades to "no
    // overlay for that source" rather than blocking the graph itself (the
    // on-call board's degrade-without-blanking precedent, E7.3c).
    listIncidents({}, ctrl.signal)
      .then((page) => {
        if (!ctrl.signal.aborted) setIncidents(page.items ?? []);
      })
      .catch(() => {
        if (!ctrl.signal.aborted) setIncidents([]);
      });

    getAssetHealth(ctrl.signal)
      .then((h) => {
        if (!ctrl.signal.aborted) setHealth(h);
      })
      .catch(() => {
        if (!ctrl.signal.aborted) setHealth(null);
      });

    return () => ctrl.abort();
  }, [reloads]);

  const overlay = useMemo(() => buildTopologyOverlay(incidents, health), [incidents, health]);
  const layout = useMemo(() => computeTopologyLayout(graph?.nodes ?? [], graph?.edges ?? []), [graph]);

  const retry = () => setReloads((n) => n + 1);

  const selectNode = (node: AssetGraphNode) => {
    setSelectedAssetId(node.asset_id);
    openSplitPanel(node.name, <TopologyNodeDetail node={node} overlay={overlayFor(overlay, node.asset_id)} />);
  };

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="The tenant's asset dependency graph, colored by open incidents and CMDB health."
        counter={graph ? `(${graph.nodes.length})` : undefined}
      >
        Topology
      </Header>

      {problem && <ErrorState problem={problem} onRetry={retry} />}

      {!problem && loading && !graph && (
        <Box margin="xxl" textAlign="center" padding="xxl">
          <div role="status" aria-busy="true">
            <Spinner size="large" /> Loading topology…
          </div>
        </Box>
      )}

      {!problem && graph && graph.truncated && (
        <Alert type="warning" header="Showing a partial graph">
          This tenant's CMDB exceeds the graph endpoint's bound — showing the first {graph.nodes.length} nodes and{' '}
          {graph.edges.length} edges (ADR-NOC-006).
        </Alert>
      )}

      {!problem && graph && graph.nodes.length === 0 && (
        <Container>
          <TopologyEmpty />
        </Container>
      )}

      {!problem && graph && graph.nodes.length > 0 && (
        <Container>
          <TopologyGraph
            layout={layout}
            overlay={overlay}
            palette={palette}
            selectedAssetId={selectedAssetId}
            onSelectNode={selectNode}
          />
        </Container>
      )}
    </SpaceBetween>
  );
}
