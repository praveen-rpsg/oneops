import { useCallback, useEffect, useMemo, useState } from 'react';
import { useOutletContext } from 'react-router-dom';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Header from '@cloudscape-design/components/header';
import Pagination from '@cloudscape-design/components/pagination';
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
import { IncidentDetailPanel } from '../components/IncidentDetail';
import { ErrorState } from '../components/States';
import { VulnFindingDetailPanel } from '../components/VulnFindingDetail';
import { humanise } from '../incidentPresentation';
import {
  ASSET_CRITICALITY_RANK,
  ASSET_CRITICALITY_TYPE,
  VULN_PRIORITY_RANK,
  VULN_PRIORITY_TYPE,
  VULN_SEVERITY_RANK,
  VULN_SEVERITY_TYPE,
  VULN_STATUS_TYPE,
} from '../vulnFindingPresentation';
import {
  listPrioritizedVulnFindings,
  listVulnFindings,
  VULN_FINDING_LIST_CAP,
  VULN_FINDING_SEVERITIES,
  VULN_FINDING_STATUSES,
} from '../vulnerabilities';
import type {
  AssetCriticality,
  VulnFindingDTO,
  VulnFindingPriority,
  VulnFindingSeverity,
  VulnFindingStatus,
} from '../vulnerabilities';

/** Client-side page size over the already-fetched, already-ranked set (see vulnerabilities.ts' VULN_FINDING_LIST_CAP). */
const PAGE_SIZE = 20;

type ViewMode = 'list' | 'prioritized';

const VIEW_OPTIONS: SegmentedControlProps.Option[] = [
  { id: 'list', text: 'All findings' },
  { id: 'prioritized', text: 'Prioritized' },
];

/**
 * One table row, normalised across both endpoints this board reads: `list`
 * rows carry only `finding`; `prioritized` rows additionally carry the
 * asset-criticality join and the computed priority — GET
 * /v1/admin/vuln-findings/prioritized's own row shape
 * (`PrioritizedVulnFindingDTO`), flattened so one `Table`/one column set
 * serves both views rather than maintaining two near-duplicate tables.
 */
interface FindingRow {
  finding: VulnFindingDTO;
  priority?: VulnFindingPriority;
  assetCriticality?: AssetCriticality;
}

const STATUS_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All statuses' },
  ...VULN_FINDING_STATUSES.map((s) => ({ value: s, label: humanise(s) })),
];

const SEVERITY_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All severities' },
  ...VULN_FINDING_SEVERITIES.map((s) => ({ value: s, label: humanise(s) })),
];

function byLastSeenDesc(a: FindingRow, b: FindingRow): number {
  return new Date(b.finding.last_seen).getTime() - new Date(a.finding.last_seen).getTime();
}

function FindingsEmpty({
  mode,
  status,
  onShowAll,
}: {
  mode: ViewMode;
  status: VulnFindingStatus | '';
  onShowAll: () => void;
}) {
  if (mode === 'prioritized') {
    return (
      <Box textAlign="center" color="inherit" padding="l">
        <SpaceBetween size="s">
          <b>No open findings</b>
          <Box variant="p" color="text-body-secondary">
            Nothing is currently open for this tenant — prioritization only ranks open findings.
          </Box>
        </SpaceBetween>
      </Box>
    );
  }
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No findings{status ? ` with status "${humanise(status)}"` : ''}</b>
        <Box variant="p" color="text-body-secondary">
          {status ? 'Nothing matches this status right now.' : 'No scanner has reported a finding for this tenant yet.'}
        </Box>
        {status && <Button onClick={onShowAll}>Show all statuses</Button>}
      </SpaceBetween>
    </Box>
  );
}

/** The clean, honest 403: the server's own refusal, never a crash or a raw dump. */
function PermissionNeeded() {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>You need tenant-admin permission to manage vulnerability findings.</b>
        <Box variant="p" color="text-body-secondary">
          Your account does not currently hold the permission OneOps&apos; vulnerability-management endpoints
          require. Ask your tenant administrator to grant it.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

/**
 * Vulnerabilities (E-SEC-UI.1): the Security console section's first screen,
 * over the E8.3 vuln-management endpoints that already exist. Lists the
 * tenant's findings (filter by status/severity, sortable), toggles to a
 * business-risk-ranked "Prioritized" view (GET .../prioritized), and drills
 * into one finding's detail — including its legal-only status transitions
 * and one-click Remediate — through the shell's `SplitPanel`, the same
 * pattern `IncidentBoardPage`/`AlertsBoardPage` established (ADR-NOC-004,
 * ADR-ACT-001). A finding's linked remediation incident opens through that
 * SAME `SplitPanel` via `IncidentDetailPanel`, mirroring `AlertsBoardPage`'s
 * "Linked incident" column exactly.
 */
export function VulnerabilitiesPage() {
  const { openSplitPanel } = useOutletContext<ShellSplitPanelContext>();

  const [mode, setMode] = useState<ViewMode>('list');
  const [statusFilter, setStatusFilter] = useState<VulnFindingStatus | ''>('');
  const [severityFilter, setSeverityFilter] = useState<VulnFindingSeverity | ''>('');
  const [rows, setRows] = useState<FindingRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);
  const [currentPageIndex, setCurrentPageIndex] = useState(1);
  const [sortState, setSortState] = useState<{ columnId: string; descending: boolean }>({
    columnId: 'severity',
    descending: false,
  });

  const reload = useCallback(() => setReloads((n) => n + 1), []);

  const openIncident = useCallback(
    (incidentId: string) => {
      openSplitPanel(`Incident ${incidentId}`, <IncidentDetailPanel incidentId={incidentId} onChanged={reload} />);
    },
    [openSplitPanel, reload],
  );

  const openFinding = useCallback(
    (row: FindingRow) => {
      openSplitPanel(
        row.finding.title,
        <VulnFindingDetailPanel findingId={row.finding.finding_id} onChanged={reload} onOpenIncident={openIncident} />,
      );
    },
    [openSplitPanel, reload, openIncident],
  );

  // Switching view mode resets paging and picks that mode's own default sort
  // column — the two views do not share every column (see `columns` below),
  // so holding a stale `TableProps.ColumnDefinition` reference across a mode
  // switch is avoided entirely by keying sort state on column `id` instead.
  useEffect(() => {
    setCurrentPageIndex(1);
    setSortState({ columnId: mode === 'prioritized' ? 'priority' : 'severity', descending: false });
  }, [mode]);

  const load = useCallback(
    (m: ViewMode, status: VulnFindingStatus | '', severity: VulnFindingSeverity | '', signal: AbortSignal) => {
      setLoading(true);
      setProblem(null);
      const request: Promise<FindingRow[]> =
        m === 'prioritized'
          ? listPrioritizedVulnFindings(VULN_FINDING_LIST_CAP, signal).then((page) =>
              (page.items ?? []).map(
                (p): FindingRow => ({ finding: p.finding, priority: p.priority, assetCriticality: p.asset_criticality }),
              ),
            )
          : listVulnFindings({ status, severity }, signal).then((page) =>
              (page.items ?? []).map((f): FindingRow => ({ finding: f })),
            );
      request
        .then((items) => setRows(items))
        .catch((err: unknown) => {
          if (signal.aborted) return;
          setProblem(
            err instanceof ApiError
              ? err.problem
              : {
                  title: m === 'prioritized' ? 'Could not load prioritized findings' : 'Could not load vulnerability findings',
                  status: 0,
                  detail: String(err),
                },
          );
          setRows([]);
        })
        .finally(() => {
          if (!signal.aborted) setLoading(false);
        });
    },
    [],
  );

  useEffect(() => {
    const ctrl = new AbortController();
    load(mode, statusFilter, severityFilter, ctrl.signal);
    return () => ctrl.abort();
  }, [mode, statusFilter, severityFilter, reloads, load]);

  const forbidden = problem !== null && problem.status === 403;

  const columns = useMemo<TableProps.ColumnDefinition<FindingRow>[]>(() => {
    const cols: TableProps.ColumnDefinition<FindingRow>[] = [
      {
        id: 'title',
        header: 'Finding',
        isRowHeader: true,
        sortingComparator: (a, b) => a.finding.title.localeCompare(b.finding.title),
        cell: (row) => (
          <div>
            <Button variant="inline-link" onClick={() => openFinding(row)}>
              {row.finding.title}
            </Button>
            <Box fontSize="body-s" color="text-body-secondary">
              {row.finding.vuln_id}
            </Box>
          </div>
        ),
      },
      {
        id: 'asset',
        header: 'Asset',
        sortingComparator: (a, b) => a.finding.asset_id.localeCompare(b.finding.asset_id),
        cell: (row) => row.finding.asset_id,
      },
    ];

    if (mode === 'prioritized') {
      // `row.priority`/`row.assetCriticality` are only ever undefined for one
      // transient render — the frame between clicking the view toggle
      // (`columns` switches to this branch immediately) and the mode's own
      // fetch resolving (`rows` is cleared in the SAME state update as the
      // mode change, see the `SegmentedControl` `onChange` below, so this is
      // ONLY reachable via a genuinely empty/stale row, never a live one) —
      // rendered as the same em-dash fallback the "Remediation" column below
      // uses, never a crash.
      cols.push(
        {
          id: 'priority',
          header: 'Priority',
          sortingComparator: (a, b) =>
            (VULN_PRIORITY_RANK[a.priority!] ?? Number.MAX_SAFE_INTEGER) - (VULN_PRIORITY_RANK[b.priority!] ?? Number.MAX_SAFE_INTEGER) ||
            byLastSeenDesc(a, b),
          cell: (row) =>
            row.priority ? (
              <StatusIndicator type={VULN_PRIORITY_TYPE[row.priority]}>{humanise(row.priority)}</StatusIndicator>
            ) : (
              <Box color="text-body-secondary">—</Box>
            ),
        },
        {
          id: 'asset_criticality',
          header: 'Asset criticality',
          sortingComparator: (a, b) =>
            (ASSET_CRITICALITY_RANK[a.assetCriticality!] ?? Number.MAX_SAFE_INTEGER) -
            (ASSET_CRITICALITY_RANK[b.assetCriticality!] ?? Number.MAX_SAFE_INTEGER),
          cell: (row) =>
            row.assetCriticality ? (
              <StatusIndicator type={ASSET_CRITICALITY_TYPE[row.assetCriticality]}>
                {humanise(row.assetCriticality)}
              </StatusIndicator>
            ) : (
              <Box color="text-body-secondary">—</Box>
            ),
        },
      );
    }

    cols.push({
      id: 'severity',
      header: 'Severity',
      sortingComparator: (a, b) => VULN_SEVERITY_RANK[a.finding.severity] - VULN_SEVERITY_RANK[b.finding.severity],
      cell: (row) => (
        <StatusIndicator type={VULN_SEVERITY_TYPE[row.finding.severity]}>{humanise(row.finding.severity)}</StatusIndicator>
      ),
    });

    if (mode === 'list') {
      cols.push({
        id: 'status',
        header: 'Status',
        sortingComparator: (a, b) => a.finding.status.localeCompare(b.finding.status),
        cell: (row) => (
          <StatusIndicator type={VULN_STATUS_TYPE[row.finding.status]}>{humanise(row.finding.status)}</StatusIndicator>
        ),
      });
    }

    cols.push(
      {
        id: 'last_seen',
        header: 'Last seen',
        sortingComparator: byLastSeenDesc,
        cell: (row) => new Date(row.finding.last_seen).toLocaleString(),
      },
      {
        id: 'scanner',
        header: 'Scanner',
        sortingComparator: (a, b) => a.finding.scanner.localeCompare(b.finding.scanner),
        cell: (row) => row.finding.scanner,
      },
      {
        id: 'remediation',
        header: 'Remediation',
        sortingComparator: (a, b) => (a.finding.remediation_incident_id ?? '').localeCompare(b.finding.remediation_incident_id ?? ''),
        cell: (row) =>
          row.finding.remediation_incident_id ? (
            <Button variant="inline-link" onClick={() => openIncident(row.finding.remediation_incident_id!)}>
              {row.finding.remediation_incident_id}
            </Button>
          ) : (
            <Box color="text-body-secondary">—</Box>
          ),
      },
    );

    return cols;
  }, [mode, openFinding, openIncident]);

  const sortingColumn = columns.find((c) => c.id === sortState.columnId) ?? columns[0];

  const sorted = useMemo(() => {
    const cmp = sortingColumn.sortingComparator ?? (() => 0);
    const dir = sortState.descending ? -1 : 1;
    return [...rows].sort((a, b) => cmp(a, b) * dir);
  }, [rows, sortingColumn, sortState.descending]);

  const pagesCount = Math.max(1, Math.ceil(sorted.length / PAGE_SIZE));
  const pageItems = sorted.slice((currentPageIndex - 1) * PAGE_SIZE, currentPageIndex * PAGE_SIZE);

  const statusSelected = STATUS_OPTIONS.find((o) => o.value === statusFilter) ?? STATUS_OPTIONS[0];
  const severitySelected = SEVERITY_OPTIONS.find((o) => o.value === severityFilter) ?? SEVERITY_OPTIONS[0];

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Vulnerability findings across your tenant's assets: scanner-reported, deduplicated, and ranked by business risk."
        counter={!forbidden ? `(${rows.length})` : undefined}
        actions={
          <SegmentedControl
            selectedId={mode}
            onChange={({ detail }) => {
              // Cleared in the SAME state update as the mode switch: `columns`
              // (keyed on `mode`) and `rows` (still shaped for the OLD mode
              // until its own fetch resolves) must never disagree for even one
              // render, or a `list`-shaped row hits a `prioritized`-only cell
              // (or vice versa) before this batch's `load` effect runs.
              setMode(detail.selectedId as ViewMode);
              setRows([]);
            }}
            options={VIEW_OPTIONS}
            label="View"
          />
        }
      >
        Vulnerabilities
      </Header>

      {mode === 'list' && !forbidden && (
        <SpaceBetween direction="horizontal" size="s" alignItems="center">
          <Select
            selectedOption={statusSelected}
            onChange={({ detail }) => setStatusFilter((detail.selectedOption.value ?? '') as VulnFindingStatus | '')}
            options={STATUS_OPTIONS}
            ariaLabel="Filter by status"
          />
          <Select
            selectedOption={severitySelected}
            onChange={({ detail }) => setSeverityFilter((detail.selectedOption.value ?? '') as VulnFindingSeverity | '')}
            options={SEVERITY_OPTIONS}
            ariaLabel="Filter by severity"
          />
        </SpaceBetween>
      )}

      {mode === 'prioritized' && !forbidden && (
        <Box color="text-body-secondary" fontSize="body-s">
          Open findings only, ranked by severity × asset criticality (ADR-SOC-007). Ties are broken by most recently
          re-detected.
        </Box>
      )}

      {rows.length >= VULN_FINDING_LIST_CAP && (
        <Box color="text-status-warning" fontSize="body-s">
          Showing the first {VULN_FINDING_LIST_CAP} findings for this filter. Narrow the filters for a complete view.
        </Box>
      )}

      {forbidden && <PermissionNeeded />}

      {!forbidden && problem && <ErrorState problem={problem} onRetry={reload} />}

      {!forbidden && !problem && (
        <Table
          items={pageItems}
          columnDefinitions={columns}
          trackBy={(row) => row.finding.finding_id}
          loading={loading}
          loadingText="Loading vulnerability findings"
          variant="container"
          sortingColumn={sortingColumn}
          sortingDescending={sortState.descending}
          onSortingChange={({ detail }) =>
            setSortState({
              columnId: (detail.sortingColumn as TableProps.ColumnDefinition<FindingRow>).id ?? sortState.columnId,
              descending: Boolean(detail.isDescending),
            })
          }
          ariaLabels={{ tableLabel: mode === 'prioritized' ? 'Vulnerability findings, prioritized' : 'Vulnerability findings' }}
          empty={<FindingsEmpty mode={mode} status={statusFilter} onShowAll={() => setStatusFilter('')} />}
          pagination={
            <Pagination
              currentPageIndex={currentPageIndex}
              pagesCount={pagesCount}
              onChange={({ detail }) => setCurrentPageIndex(detail.currentPageIndex)}
            />
          }
        />
      )}
    </SpaceBetween>
  );
}
