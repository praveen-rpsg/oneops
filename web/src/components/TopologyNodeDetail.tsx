import Box from '@cloudscape-design/components/box';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import type { AssetCriticality, AssetEnvironment, AssetGraphNode, AssetStatus } from '../assetGraph';
import { humanise } from '../incidentPresentation';
import type { NodeOverlay } from '../topologyOverlay';

const ASSET_STATUS_TYPE: Record<AssetStatus, StatusIndicatorProps.Type> = {
  planned: 'pending',
  active: 'success',
  maintenance: 'in-progress',
  retired: 'stopped',
};

const CRITICALITY_TYPE: Record<AssetCriticality, StatusIndicatorProps.Type> = {
  critical: 'error',
  high: 'error',
  medium: 'warning',
  low: 'info',
  unknown: 'pending',
};

const ENVIRONMENT_TYPE: Record<AssetEnvironment, StatusIndicatorProps.Type> = {
  production: 'success',
  staging: 'info',
  development: 'pending',
  unknown: 'pending',
};

/**
 * The `SplitPanel` content for one topology node — no re-fetch: the graph
 * response already carries every field shown here (E7.3b-1's
 * AssetGraphNode), and the incident/health overlay is composed client-side
 * (E7.3b-2, ADR-NOC-007).
 */
export function TopologyNodeDetail({ node, overlay }: { node: AssetGraphNode; overlay: NodeOverlay }) {
  return (
    <SpaceBetween size="l">
      <KeyValuePairs
        columns={2}
        items={[
          { label: 'Asset ID', value: node.asset_id },
          { label: 'Type', value: node.type },
          { label: 'Status', value: <StatusIndicator type={ASSET_STATUS_TYPE[node.status]}>{humanise(node.status)}</StatusIndicator> },
          {
            label: 'Environment',
            value: <StatusIndicator type={ENVIRONMENT_TYPE[node.environment]}>{humanise(node.environment)}</StatusIndicator>,
          },
          {
            label: 'Criticality',
            value: <StatusIndicator type={CRITICALITY_TYPE[node.criticality]}>{humanise(node.criticality)}</StatusIndicator>,
          },
          {
            label: 'Open incidents',
            value:
              overlay.openIncidentCount > 0 ? (
                <StatusIndicator type="error">
                  {overlay.openIncidentCount} open incident{overlay.openIncidentCount === 1 ? '' : 's'}
                </StatusIndicator>
              ) : (
                <StatusIndicator type="success">None</StatusIndicator>
              ),
          },
        ]}
      />
      {overlay.healthIssues.length > 0 && (
        <div>
          <Box variant="awsui-key-label">CMDB health</Box>
          <SpaceBetween size="xs">
            {overlay.healthIssues.map((issue) => (
              <StatusIndicator key={issue} type="warning">
                {issue}
              </StatusIndicator>
            ))}
          </SpaceBetween>
        </div>
      )}
    </SpaceBetween>
  );
}
