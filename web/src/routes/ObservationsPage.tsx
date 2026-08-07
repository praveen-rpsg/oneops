import { useCallback, useEffect, useMemo, useState } from 'react';
import { useOutletContext } from 'react-router-dom';
import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Select from '@cloudscape-design/components/select';
import type { SelectProps } from '@cloudscape-design/components/select';
import SegmentedControl from '@cloudscape-design/components/segmented-control';
import type { SegmentedControlProps } from '@cloudscape-design/components/segmented-control';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import type { TableProps } from '@cloudscape-design/components/table';
import type { ShellSplitPanelContext } from '../Shell';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { getAssetGraph } from '../assetGraph';
import type { AssetGraphNode } from '../assetGraph';
import { ErrorState } from '../components/States';
import { humanise } from '../incidentPresentation';
import { OBSERVATION_SEVERITY_TYPE } from '../securityRulePresentation';
import { securityRuleObservationTypeError } from '../securityRules';
import type { ObservationSeverity } from '../securityRules';
import { OBSERVATION_SEVERITIES, SECURITY_OBSERVATION_DEFAULT_LIMIT, querySecurityObservations } from '../securityObservations';
import type { SecurityObservationDTO } from '../securityObservations';

type RangeId = '1h' | '24h' | '7d';

const RANGE_OPTIONS: SegmentedControlProps.Option[] = [
  { id: '1h', text: 'Last 1h' },
  { id: '24h', text: 'Last 24h' },
  { id: '7d', text: 'Last 7d' },
];

function windowFor(rangeId: RangeId, now: Date): { from: Date; to: Date } {
  const hours = rangeId === '1h' ? 1 : rangeId === '24h' ? 24 : 24 * 7;
  return { from: new Date(now.getTime() - hours * 3_600_000), to: now };
}

const SEVERITY_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All severities' },
  ...OBSERVATION_SEVERITIES.map((s) => ({ value: s, label: humanise(s) })),
];

/** A row's natural key: security_observation's own uniqueness constraint (tenant_id, asset_id, observation_type, source, observed_at) minus the tenant, which is already fixed for the whole query — see SecurityObservationRepository.QueryRange's doc comment. */
function observationKey(o: SecurityObservationDTO): string {
  return `${o.asset_id}|${o.observation_type}|${o.source}|${o.observed_at}`;
}

/** Compact key=value chips, truncated — the full set is always available in the row's detail panel. */
function AttributesSummary({ attributes }: { attributes: Record<string, string> }) {
  const entries = Object.entries(attributes ?? {});
  if (entries.length === 0) return <Box color="text-body-secondary">—</Box>;
  const shown = entries.slice(0, 3);
  const remaining = entries.length - shown.length;
  return (
    <SpaceBetween direction="horizontal" size="xxs">
      {shown.map(([k, v]) => (
        <Badge key={k} color="grey">{`${k}=${v}`}</Badge>
      ))}
      {remaining > 0 && (
        <Box color="text-body-secondary" fontSize="body-s">
          +{remaining} more
        </Box>
      )}
    </SpaceBetween>
  );
}

/**
 * The read-only detail panel for one observation (E-SEC-UI.4). There is no
 * `GET .../security-observations/{id}` — a SecurityObservation carries no
 * identifier at all (an append-only fact, not a reified entity; see
 * domain.SecurityObservation's own doc comment) — so this renders the row
 * already held client-side rather than issuing a second fetch, the one
 * detail panel in this console that does not re-GET its subject.
 */
function ObservationDetailPanel({ observation }: { observation: SecurityObservationDTO }) {
  const entries = Object.entries(observation.attributes ?? {});
  return (
    <SpaceBetween size="l">
      <Box color="text-body-secondary" fontSize="body-s">
        Read-only: security_observation is an append-only fact table (E8.1a) with no management API — nothing here
        can be edited or deleted from the console.
      </Box>
      <KeyValuePairs
        columns={2}
        items={[
          { label: 'Asset', value: observation.asset_id },
          { label: 'Observation type', value: observation.observation_type },
          { label: 'Source', value: observation.source },
          {
            label: 'Severity',
            value: (
              <StatusIndicator type={OBSERVATION_SEVERITY_TYPE[observation.severity]}>
                {humanise(observation.severity)}
              </StatusIndicator>
            ),
          },
          { label: 'Observed at', value: new Date(observation.observed_at).toLocaleString() },
        ]}
      />
      <Box>
        <Box variant="awsui-key-label">Attributes</Box>
        {entries.length === 0 ? (
          <Box color="text-body-secondary" padding={{ top: 'xs' }}>
            No attributes on this observation.
          </Box>
        ) : (
          <KeyValuePairs columns={2} items={entries.map(([k, v]) => ({ label: k, value: v }))} />
        )}
      </Box>
    </SpaceBetween>
  );
}

/** The clean, honest 403: the server's own refusal, never a crash or a raw dump. */
function PermissionNeeded() {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>You need tenant-admin permission to view security observations.</b>
        <Box variant="p" color="text-body-secondary">
          Your account does not currently hold the permission OneOps&apos; security-observation query endpoint
          requires. Ask your tenant administrator to grant it.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

function ObservationsEmpty({ ready }: { ready: boolean }) {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No observations</b>
        <Box variant="p" color="text-body-secondary">
          {ready
            ? 'No security observations were recorded for this asset and observation type in the selected window.'
            : 'Choose an asset and an observation type, then Load, to query this append-only fact table.'}
        </Box>
      </SpaceBetween>
    </Box>
  );
}

/**
 * The Observations screen (E-SEC-UI.4): a read-only view over
 * `GET /v1/admin/security-observations`, the SOC epic's fact-ingestion layer
 * (E8.1a). CONFIRMED CONTRACT: this is a bounded RANGE query over ONE
 * asset's ONE observation type — `asset_id`, `observation_type`, `from` and
 * `to` are ALL required server-side (querySecurityObservations,
 * handlers_security_observations.go:151-212) — there is no "list every
 * observation" mode. The UX is built around that: an asset picker, a
 * required observation-type field, and a time-range control (default last
 * 24h), committed via "Load" (mirroring DashboardsPage's own asset+metric
 * picker for the identical reason: the query cannot run until both are
 * chosen). Severity has no server-side filter on this endpoint — filtered
 * client-side over the fetched page. NO mutate UI anywhere on this screen:
 * security_observation is append-only telemetry with no PATCH/DELETE.
 */
export function ObservationsPage() {
  const { openSplitPanel } = useOutletContext<ShellSplitPanelContext>();

  const [assets, setAssets] = useState<AssetGraphNode[]>([]);
  const [assetsProblem, setAssetsProblem] = useState<ProblemDetail | null>(null);
  const [selectedAssetId, setSelectedAssetId] = useState('');

  const [observationTypeDraft, setObservationTypeDraft] = useState('');
  const [observationType, setObservationType] = useState('');
  const [rangeId, setRangeId] = useState<RangeId>('24h');
  const [severityFilter, setSeverityFilter] = useState<ObservationSeverity | ''>('');

  const [rawItems, setRawItems] = useState<SecurityObservationDTO[]>([]);
  const [loading, setLoading] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);

  const observationTypeError = securityRuleObservationTypeError(observationTypeDraft);
  const canLoad = selectedAssetId.length > 0 && observationTypeDraft.trim().length > 0 && !observationTypeError;

  useEffect(() => {
    const ctrl = new AbortController();
    getAssetGraph(ctrl.signal)
      .then((graph) => {
        const nodes = [...graph.nodes].sort((a, b) => a.name.localeCompare(b.name));
        setAssets(nodes);
        setSelectedAssetId((prev) => prev || nodes[0]?.asset_id || '');
        setAssetsProblem(null);
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setAssetsProblem(err instanceof ApiError ? err.problem : { title: 'Could not load assets', status: 0, detail: String(err) });
      });
    return () => ctrl.abort();
  }, []);

  const reload = useCallback(() => setReloads((n) => n + 1), []);

  const load = useCallback(() => {
    if (!observationTypeDraft.trim() || observationTypeError) return;
    setObservationType(observationTypeDraft.trim());
    reload();
  }, [observationTypeDraft, observationTypeError, reload]);

  useEffect(() => {
    if (!selectedAssetId || !observationType) {
      setRawItems([]);
      setProblem(null);
      return;
    }
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);
    const { from, to } = windowFor(rangeId, new Date());
    querySecurityObservations(selectedAssetId, observationType, from, to, {}, ctrl.signal)
      .then((resp) => setRawItems(resp.items ?? []))
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setRawItems([]);
        setProblem(err instanceof ApiError ? err.problem : { title: 'Could not load observations', status: 0, detail: String(err) });
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });
    return () => ctrl.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [selectedAssetId, observationType, rangeId, reloads]);

  const openObservation = useCallback(
    (o: SecurityObservationDTO) => {
      openSplitPanel(`${o.observation_type} · ${new Date(o.observed_at).toLocaleString()}`, <ObservationDetailPanel observation={o} />);
    },
    [openSplitPanel],
  );

  const columns = useMemo<TableProps.ColumnDefinition<SecurityObservationDTO>[]>(
    () => [
      {
        id: 'observed_at',
        header: 'Observed at',
        isRowHeader: true,
        sortingComparator: (a, b) => a.observed_at.localeCompare(b.observed_at),
        cell: (o) => (
          <Button variant="inline-link" onClick={() => openObservation(o)}>
            {new Date(o.observed_at).toLocaleString()}
          </Button>
        ),
      },
      {
        id: 'observation_type',
        header: 'Observation type',
        sortingComparator: (a, b) => a.observation_type.localeCompare(b.observation_type),
        cell: (o) => o.observation_type,
      },
      {
        id: 'source',
        header: 'Source',
        sortingComparator: (a, b) => a.source.localeCompare(b.source),
        cell: (o) => o.source,
      },
      {
        id: 'severity',
        header: 'Severity',
        sortingComparator: (a, b) => a.severity.localeCompare(b.severity),
        cell: (o) => <StatusIndicator type={OBSERVATION_SEVERITY_TYPE[o.severity]}>{humanise(o.severity)}</StatusIndicator>,
      },
      {
        id: 'asset_id',
        header: 'Asset',
        sortingComparator: (a, b) => a.asset_id.localeCompare(b.asset_id),
        cell: (o) => o.asset_id,
      },
      {
        id: 'attributes',
        header: 'Attributes',
        cell: (o) => <AttributesSummary attributes={o.attributes} />,
      },
    ],
    [openObservation],
  );

  const [sortState, setSortState] = useState<{ column: TableProps.ColumnDefinition<SecurityObservationDTO>; descending: boolean }>(
    () => ({ column: columns[0], descending: true }), // most recent first — the default read order.
  );

  const filtered = useMemo(
    () => rawItems.filter((o) => !severityFilter || o.severity === severityFilter),
    [rawItems, severityFilter],
  );

  const sorted = useMemo(() => {
    const cmp = sortState.column.sortingComparator ?? (() => 0);
    const dir = sortState.descending ? -1 : 1;
    return [...filtered].sort((a, b) => cmp(a, b) * dir);
  }, [filtered, sortState]);

  const assetOptions: SelectProps.Option[] = assets.map((a) => ({ value: a.asset_id, label: `${a.name} (${a.asset_id})` }));
  const selectedAssetOption = assetOptions.find((o) => o.value === selectedAssetId) ?? null;
  const selectedSeverityOption = SEVERITY_OPTIONS.find((o) => o.value === severityFilter) ?? SEVERITY_OPTIONS[0];
  const forbidden = problem !== null && problem.status === 403;
  const ready = Boolean(selectedAssetId && observationType);

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Append-only security telemetry tied to Configuration Items (E8.1a). Read-only — no create, edit or delete here."
      >
        Observations
      </Header>

      {assetsProblem && <ErrorState problem={assetsProblem} onRetry={reload} />}

      <SpaceBetween direction="horizontal" size="s" alignItems="end">
        <FormField label="Asset">
          <Select
            selectedOption={selectedAssetOption}
            onChange={({ detail }) => setSelectedAssetId(detail.selectedOption.value ?? '')}
            options={assetOptions}
            placeholder="Select an asset"
            empty="No assets in this tenant"
            ariaLabel="Asset"
            disabled={assets.length === 0}
          />
        </FormField>
        <FormField label="Observation type" errorText={observationTypeError}>
          <Input
            value={observationTypeDraft}
            onChange={({ detail }) => setObservationTypeDraft(detail.value)}
            onKeyDown={({ detail }) => {
              if (detail.key === 'Enter') load();
            }}
            placeholder="e.g. port_scan"
            ariaLabel="Observation type"
          />
        </FormField>
        <Button onClick={load} disabled={!canLoad}>
          Load
        </Button>
        <FormField label="Time range">
          <SegmentedControl
            selectedId={rangeId}
            onChange={({ detail }) => setRangeId(detail.selectedId as RangeId)}
            options={RANGE_OPTIONS}
            label="Time range"
          />
        </FormField>
        {ready && !forbidden && (
          <FormField label="Severity">
            <Select
              selectedOption={selectedSeverityOption}
              onChange={({ detail }) => setSeverityFilter((detail.selectedOption.value ?? '') as ObservationSeverity | '')}
              options={SEVERITY_OPTIONS}
              ariaLabel="Filter by severity"
            />
          </FormField>
        )}
      </SpaceBetween>

      {ready && !forbidden && !problem && (
        <Box color="text-body-secondary" fontSize="body-s">
          {filtered.length} of {rawItems.length} observation{rawItems.length === 1 ? '' : 's'} for {selectedAssetId} ·{' '}
          {observationType}
        </Box>
      )}

      {ready && !forbidden && rawItems.length >= SECURITY_OBSERVATION_DEFAULT_LIMIT && (
        <Box color="text-status-warning" fontSize="body-s">
          Showing the most recent {SECURITY_OBSERVATION_DEFAULT_LIMIT} observations in this window — narrow the time
          range to see more.
        </Box>
      )}

      {forbidden && <PermissionNeeded />}

      {!forbidden && problem && <ErrorState problem={problem} onRetry={reload} />}

      {!forbidden && !problem && (
        <Table
          items={sorted}
          columnDefinitions={columns}
          trackBy={observationKey}
          loading={loading}
          loadingText="Loading observations"
          variant="container"
          sortingColumn={sortState.column}
          sortingDescending={sortState.descending}
          onSortingChange={({ detail }) =>
            setSortState({
              column: detail.sortingColumn as TableProps.ColumnDefinition<SecurityObservationDTO>,
              descending: Boolean(detail.isDescending),
            })
          }
          ariaLabels={{ tableLabel: 'Observations' }}
          empty={<ObservationsEmpty ready={ready} />}
        />
      )}
    </SpaceBetween>
  );
}
