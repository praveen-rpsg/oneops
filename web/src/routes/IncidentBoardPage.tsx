import { useCallback, useEffect, useMemo, useState } from 'react';
import { useOutletContext } from 'react-router-dom';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Pagination from '@cloudscape-design/components/pagination';
import Select from '@cloudscape-design/components/select';
import type { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import type { TableProps } from '@cloudscape-design/components/table';
import Textarea from '@cloudscape-design/components/textarea';
import type { ShellSplitPanelContext } from '../Shell';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { IncidentDetailPanel } from '../components/IncidentDetail';
import { ErrorState } from '../components/States';
import type { GroupedIncident } from '../incidentGrouping';
import { groupIncidents } from '../incidentGrouping';
import { ageLabel, humanise, SEVERITY_RANK, SEVERITY_TYPE, STATUS_RANK, STATUS_TYPE } from '../incidentPresentation';
import {
  createIncident,
  INCIDENT_LIST_CAP,
  INCIDENT_SEVERITIES,
  INCIDENT_STATUSES,
  listIncidents,
} from '../incidents';
import type { IncidentDTO, IncidentSeverity, IncidentStatus } from '../incidents';

/** Client-side page size over the already-fetched, already-grouped set (see incidents.ts' INCIDENT_LIST_CAP). */
const PAGE_SIZE = 20;

const STATUS_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All statuses' },
  ...INCIDENT_STATUSES.map((s) => ({ value: s, label: humanise(s) })),
];

function byCreatedAtAsc(a: IncidentDTO, b: IncidentDTO): number {
  return new Date(a.created_at).getTime() - new Date(b.created_at).getTime();
}

/** Severity first, then age (older first) — the board's default read order. */
function bySeverityThenAge(a: GroupedIncident, b: GroupedIncident): number {
  return SEVERITY_RANK[a.severity] - SEVERITY_RANK[b.severity] || byCreatedAtAsc(a, b);
}

function IncidentsEmpty({ status, onShowAll }: { status: IncidentStatus | ''; onShowAll: () => void }) {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No incidents{status ? ` with status "${humanise(status)}"` : ''}</b>
        <Box variant="p" color="text-body-secondary">
          {status
            ? 'Nothing is open in this status right now.'
            : 'Nothing has been filed for this tenant yet.'}
        </Box>
        {status && <Button onClick={onShowAll}>Show all statuses</Button>}
      </SpaceBetween>
    </Box>
  );
}

const SEVERITY_OPTIONS: SelectProps.Option[] = INCIDENT_SEVERITIES.map((s) => ({ value: s, label: humanise(s) }));

/**
 * "Create incident" (ADR-ACT-001 §4): title required, everything else
 * optional, POSTs to `createIncident` and hands the server's own created
 * record back to the caller — the board reloads and, if the caller asks,
 * opens the new incident's detail immediately.
 */
function CreateIncidentModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (inc: IncidentDTO) => void;
}) {
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [severity, setSeverity] = useState<IncidentSeverity>('medium');
  const [assetId, setAssetId] = useState('');
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const trimmedTitle = title.trim();
  const canSubmit = trimmedTitle.length > 0 && !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      const created = await createIncident({
        title: trimmedTitle,
        description: description.trim() || undefined,
        severity,
        asset_id: assetId.trim() || undefined,
      });
      onCreated(created);
    } catch (err) {
      setProblem(
        err instanceof ApiError ? err.problem : { title: 'Create failed', status: 0, detail: String(err) },
      );
    } finally {
      setBusy(false);
    }
  }, [canSubmit, trimmedTitle, description, severity, assetId, onCreated]);

  return (
    <Modal
      visible
      header="Create incident"
      onDismiss={() => {
        if (!busy) onClose();
      }}
      closeAriaLabel="Dismiss"
      footer={
        <Box float="right">
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button variant="primary" loading={busy} disabled={!canSubmit} onClick={() => void submit()}>
              Create
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Form>
        <SpaceBetween size="m">
          {problem && (
            <div role="alert">
              <Alert type="error" header="Could not create the incident">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <FormField label="Title" errorText={title.length > 0 && trimmedTitle.length === 0 ? 'Title is required.' : undefined}>
            <Input value={title} onChange={({ detail }) => setTitle(detail.value)} disabled={busy} ariaLabel="Title" placeholder="Required" />
          </FormField>
          <FormField label="Description" description="Optional">
            <Textarea
              value={description}
              onChange={({ detail }) => setDescription(detail.value)}
              disabled={busy}
              ariaLabel="Description"
            />
          </FormField>
          <FormField label="Severity">
            <Select
              selectedOption={SEVERITY_OPTIONS.find((o) => o.value === severity) ?? SEVERITY_OPTIONS[0]}
              onChange={({ detail }) => setSeverity((detail.selectedOption.value ?? 'medium') as IncidentSeverity)}
              options={SEVERITY_OPTIONS}
              disabled={busy}
              ariaLabel="Severity"
            />
          </FormField>
          <FormField label="Asset ID" description="Optional — links this incident to a Configuration Item">
            <Input
              value={assetId}
              onChange={({ detail }) => setAssetId(detail.value)}
              disabled={busy}
              ariaLabel="Asset ID"
            />
          </FormField>
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

/**
 * The incident board: every open-class incident (default) or every incident
 * (status cleared) for the caller's tenant, root incidents with their
 * collateral nested underneath via `Table`'s `expandableRows`, drilling into
 * detail + timeline through the shell's `SplitPanel` (ADR-NOC-004), and — new
 * in E-ACT.1 — acknowledging/transitioning/assigning from that same panel
 * plus creating new incidents from this board (ADR-ACT-001).
 */
export function IncidentBoardPage() {
  const { openSplitPanel } = useOutletContext<ShellSplitPanelContext>();

  const [statusFilter, setStatusFilter] = useState<IncidentStatus | ''>('open');
  const [rawItems, setRawItems] = useState<IncidentDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);
  const [expandedItems, setExpandedItems] = useState<GroupedIncident[]>([]);
  const [currentPageIndex, setCurrentPageIndex] = useState(1);
  const [createOpen, setCreateOpen] = useState(false);

  /** Reruns the board's own fetch — shared by the error-retry button, "action changed something" callbacks from the detail panel, and a successful create. */
  const reload = useCallback(() => setReloads((n) => n + 1), []);

  const openIncident = useCallback(
    (inc: IncidentDTO) => {
      openSplitPanel(inc.title, <IncidentDetailPanel incidentId={inc.incident_id} onChanged={reload} />);
    },
    [openSplitPanel, reload],
  );

  // Stable column identities across renders (openIncident is itself stable) so
  // Table's own "which column is sorted" highlighting keeps matching by
  // reference, exactly the same object this component hands back to
  // `sortingColumn` below.
  const columns = useMemo<TableProps.ColumnDefinition<GroupedIncident>[]>(
    () => [
      {
        id: 'title',
        header: 'Title',
        isRowHeader: true,
        sortingComparator: (a, b) => a.title.localeCompare(b.title),
        cell: (inc) => (
          <div>
            <Button variant="inline-link" onClick={() => openIncident(inc)}>
              {inc.title}
            </Button>
            <Box fontSize="body-s" color="text-body-secondary">
              {inc.incident_id}
              {inc.root_incident_id && ` · collateral of ${inc.root_incident_id}`}
            </Box>
          </div>
        ),
      },
      {
        id: 'severity',
        header: 'Severity',
        sortingComparator: bySeverityThenAge,
        cell: (inc) => <StatusIndicator type={SEVERITY_TYPE[inc.severity]}>{humanise(inc.severity)}</StatusIndicator>,
      },
      {
        id: 'status',
        header: 'Status',
        sortingComparator: (a, b) => STATUS_RANK[a.status] - STATUS_RANK[b.status],
        cell: (inc) => <StatusIndicator type={STATUS_TYPE[inc.status]}>{humanise(inc.status)}</StatusIndicator>,
      },
      {
        id: 'asset',
        header: 'Asset',
        sortingComparator: (a, b) => (a.asset_id ?? '').localeCompare(b.asset_id ?? ''),
        cell: (inc) => inc.asset_id ?? <Box color="text-body-secondary">Unlinked</Box>,
      },
      {
        id: 'age',
        header: 'Age',
        sortingComparator: byCreatedAtAsc,
        cell: (inc) => <span title={new Date(inc.created_at).toLocaleString()}>{ageLabel(inc.created_at)}</span>,
      },
      {
        id: 'assignee',
        header: 'Assignee',
        sortingComparator: (a, b) => (a.assignee_user_id ?? '').localeCompare(b.assignee_user_id ?? ''),
        cell: (inc) => inc.assignee_user_id ?? <Box color="text-body-secondary">Unassigned</Box>,
      },
    ],
    [openIncident],
  );

  const [sortState, setSortState] = useState<{ column: TableProps.ColumnDefinition<GroupedIncident>; descending: boolean }>(
    () => ({ column: columns[1], descending: false }), // severity — the default read order.
  );

  const load = useCallback((status: IncidentStatus | '', signal: AbortSignal) => {
    setLoading(true);
    setProblem(null);
    listIncidents({ status }, signal)
      .then((page) => {
        const items = page.items ?? [];
        setRawItems(items);
        setExpandedItems(groupIncidents(items).filter((g) => g.children.length > 0));
        setCurrentPageIndex(1);
      })
      .catch((err: unknown) => {
        if (signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load incidents', status: 0, detail: String(err) },
        );
        setRawItems([]);
      })
      .finally(() => {
        if (!signal.aborted) setLoading(false);
      });
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    load(statusFilter, ctrl.signal);
    return () => ctrl.abort();
  }, [statusFilter, reloads, load]);

  const grouped = useMemo(() => groupIncidents(rawItems), [rawItems]);

  const sorted = useMemo(() => {
    const cmp = sortState.column.sortingComparator ?? (() => 0);
    const dir = sortState.descending ? -1 : 1;
    return [...grouped].sort((a, b) => cmp(a, b) * dir);
  }, [grouped, sortState]);

  const pagesCount = Math.max(1, Math.ceil(sorted.length / PAGE_SIZE));
  const pageItems = sorted.slice((currentPageIndex - 1) * PAGE_SIZE, currentPageIndex * PAGE_SIZE);

  const selectedOption = STATUS_OPTIONS.find((o) => o.value === statusFilter) ?? STATUS_OPTIONS[0];

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Open incidents, grouped root-cause to collateral. Click a title for detail and timeline."
        counter={`(${rawItems.length})`}
        actions={<Button onClick={() => setCreateOpen(true)}>Create incident</Button>}
      >
        Incidents
      </Header>

      <SpaceBetween direction="horizontal" size="s" alignItems="center">
        <Select
          selectedOption={selectedOption}
          onChange={({ detail }) => setStatusFilter((detail.selectedOption.value ?? '') as IncidentStatus | '')}
          options={STATUS_OPTIONS}
          ariaLabel="Filter by status"
        />
        <Box color="text-body-secondary" fontSize="body-s">
          {rawItems.length} {statusFilter} incident{rawItems.length === 1 ? '' : 's'}
        </Box>
      </SpaceBetween>

      {rawItems.length >= INCIDENT_LIST_CAP && (
        <Box color="text-status-warning" fontSize="body-s">
          Showing the first {INCIDENT_LIST_CAP} incidents for this filter — a root and its collateral could be
          split across this bound (see ADR-NOC-004). Narrow the status filter for a complete grouping.
        </Box>
      )}

      {problem && <ErrorState problem={problem} onRetry={reload} />}

      {!problem && (
        <Table
          items={pageItems}
          columnDefinitions={columns}
          trackBy="incident_id"
          loading={loading}
          loadingText="Loading incidents"
          variant="container"
          sortingColumn={sortState.column}
          sortingDescending={sortState.descending}
          onSortingChange={({ detail }) =>
            setSortState({
              column: detail.sortingColumn as TableProps.ColumnDefinition<GroupedIncident>,
              descending: Boolean(detail.isDescending),
            })
          }
          expandableRows={{
            getItemChildren: (inc) => inc.children,
            isItemExpandable: (inc) => inc.children.length > 0,
            expandedItems,
            onExpandableItemToggle: ({ detail }) => {
              setExpandedItems((prev) =>
                detail.expanded
                  ? [...prev, detail.item]
                  : prev.filter((i) => i.incident_id !== detail.item.incident_id),
              );
            },
          }}
          ariaLabels={{ tableLabel: 'Incidents, grouped root to collateral' }}
          empty={<IncidentsEmpty status={statusFilter} onShowAll={() => setStatusFilter('')} />}
          pagination={
            <Pagination
              currentPageIndex={currentPageIndex}
              pagesCount={pagesCount}
              onChange={({ detail }) => setCurrentPageIndex(detail.currentPageIndex)}
            />
          }
        />
      )}

      {createOpen && (
        <CreateIncidentModal
          onClose={() => setCreateOpen(false)}
          onCreated={(created) => {
            setCreateOpen(false);
            reload();
            openIncident(created);
          }}
        />
      )}
    </SpaceBetween>
  );
}
