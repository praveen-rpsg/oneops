import { useCallback, useEffect, useMemo, useState } from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import DateInput from '@cloudscape-design/components/date-input';
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
import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import type { TableProps } from '@cloudscape-design/components/table';
import TimeInput from '@cloudscape-design/components/time-input';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { ErrorState } from '../components/States';
import { humanise } from '../incidentPresentation';
import {
  MAINTENANCE_WINDOW_LIST_CAP,
  combineDateAndTime,
  computeMaintenanceWindowStatus,
  createMaintenanceWindow,
  deleteMaintenanceWindow,
  listMaintenanceWindows,
  maintenanceWindowAssetIdError,
  maintenanceWindowRangeError,
  maintenanceWindowReasonError,
} from '../maintenanceWindows';
import type { MaintenanceWindowDTO, MaintenanceWindowStatus } from '../maintenanceWindows';

/** Client-side page size over the already-fetched set (see maintenanceWindows.ts' MAINTENANCE_WINDOW_LIST_CAP). */
const PAGE_SIZE = 20;

const STATUS_TYPE: Record<MaintenanceWindowStatus, StatusIndicatorProps.Type> = {
  upcoming: 'pending',
  active: 'in-progress',
  expired: 'stopped',
};

const STATUS_RANK: Record<MaintenanceWindowStatus, number> = { active: 0, upcoming: 1, expired: 2 };

const STATUS_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All statuses' },
  { value: 'active', label: 'Active now' },
  { value: 'upcoming', label: 'Upcoming' },
  { value: 'expired', label: 'Expired' },
];

function MaintenanceWindowsEmpty({ hasFilter, onClear }: { hasFilter: boolean; onClear: () => void }) {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No maintenance windows{hasFilter ? ' match this filter' : ''}</b>
        <Box variant="p" color="text-body-secondary">
          {hasFilter
            ? 'Try widening the status filter.'
            : 'No maintenance windows have been declared for this tenant yet.'}
        </Box>
        {hasFilter && <Button onClick={onClear}>Clear filter</Button>}
      </SpaceBetween>
    </Box>
  );
}

/**
 * "Schedule maintenance" (E-ACT.3): asset_id (free text, the same precedent
 * ADR-ACT-002 set for alert_rule's own asset_id field — no asset picker
 * exists anywhere in this console), a starts-at and ends-at date+time pair,
 * and an optional reason. Client-validated before submit, mirroring
 * `domain.MaintenanceWindow.Validate`'s own bounds
 * (`maintenanceWindows.ts`'s validator functions) — in particular the
 * half-open [starts_at, ends_at) rule: ends_at must be strictly after
 * starts_at.
 */
function CreateMaintenanceWindowModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (win: MaintenanceWindowDTO) => void;
}) {
  const [assetId, setAssetId] = useState('');
  const [startsDate, setStartsDate] = useState('');
  const [startsTime, setStartsTime] = useState('');
  const [endsDate, setEndsDate] = useState('');
  const [endsTime, setEndsTime] = useState('');
  const [reason, setReason] = useState('');
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const assetIdError = maintenanceWindowAssetIdError(assetId);
  const reasonError = maintenanceWindowReasonError(reason);

  const startsAt = combineDateAndTime(startsDate, startsTime);
  const endsAt = combineDateAndTime(endsDate, endsTime);
  const rangeError = maintenanceWindowRangeError(startsAt, endsAt);

  const trimmedAssetId = assetId.trim();
  const canSubmit =
    trimmedAssetId.length > 0 &&
    !assetIdError &&
    !reasonError &&
    Boolean(startsAt) &&
    Boolean(endsAt) &&
    !rangeError &&
    !busy;

  const submit = useCallback(async () => {
    if (!canSubmit || !startsAt || !endsAt) return;
    setBusy(true);
    setProblem(null);
    try {
      const created = await createMaintenanceWindow({
        asset_id: trimmedAssetId,
        starts_at: startsAt.toISOString(),
        ends_at: endsAt.toISOString(),
        reason: reason.trim() || undefined,
      });
      onCreated(created);
    } catch (err) {
      setProblem(err instanceof ApiError ? err.problem : { title: 'Create failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [canSubmit, trimmedAssetId, startsAt, endsAt, reason, onCreated]);

  return (
    <Modal
      visible
      header="Schedule maintenance"
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
              Schedule
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Form>
        <SpaceBetween size="m">
          {problem && (
            <div role="alert">
              <Alert type="error" header="Could not schedule the maintenance window">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <FormField label="Asset ID" errorText={assetIdError}>
            <Input value={assetId} onChange={({ detail }) => setAssetId(detail.value)} disabled={busy} ariaLabel="Asset ID" placeholder="Required" />
          </FormField>
          <FormField label="Starts at" description="Half-open: a firing exactly at this instant IS suppressed.">
            <SpaceBetween direction="horizontal" size="xs">
              <DateInput
                value={startsDate}
                onChange={({ detail }) => setStartsDate(detail.value)}
                disabled={busy}
                ariaLabel="Starts at date"
                placeholder="YYYY/MM/DD"
              />
              <TimeInput
                value={startsTime}
                onChange={({ detail }) => setStartsTime(detail.value)}
                disabled={busy}
                format="hh:mm"
                use24Hour
                ariaLabel="Starts at time"
                placeholder="hh:mm"
              />
            </SpaceBetween>
          </FormField>
          <FormField
            label="Ends at"
            description="Half-open: a firing exactly at this instant is NOT suppressed."
            errorText={rangeError}
          >
            <SpaceBetween direction="horizontal" size="xs">
              <DateInput
                value={endsDate}
                onChange={({ detail }) => setEndsDate(detail.value)}
                disabled={busy}
                ariaLabel="Ends at date"
                placeholder="YYYY/MM/DD"
              />
              <TimeInput
                value={endsTime}
                onChange={({ detail }) => setEndsTime(detail.value)}
                disabled={busy}
                format="hh:mm"
                use24Hour
                ariaLabel="Ends at time"
                placeholder="hh:mm"
              />
            </SpaceBetween>
          </FormField>
          <FormField label="Reason" description="Optional — free text, e.g. 'patching window'." errorText={reasonError}>
            <Input value={reason} onChange={({ detail }) => setReason(detail.value)} disabled={busy} ariaLabel="Reason" placeholder="Optional" />
          </FormField>
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

/**
 * The maintenance board: every declared maintenance window for the caller's
 * tenant (bounded at MAINTENANCE_WINDOW_LIST_CAP), with an active/upcoming/
 * expired status computed client-side from `now` against each window's own
 * half-open [starts_at, ends_at) bounds — the DTO itself carries no status
 * field. New in E-ACT.3 (ADR-ACT-004): "Schedule maintenance" from this
 * board, and "Cancel" per row. There is no drill-down/detail surface — the
 * list endpoint already returns the full DTO, and this story's only per-row
 * action (Cancel) does not need one.
 */
export function MaintenanceBoardPage() {
  const [statusFilter, setStatusFilter] = useState<MaintenanceWindowStatus | ''>('');
  const [rawItems, setRawItems] = useState<MaintenanceWindowDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);
  const [currentPageIndex, setCurrentPageIndex] = useState(1);
  const [createOpen, setCreateOpen] = useState(false);

  const [cancelTarget, setCancelTarget] = useState<MaintenanceWindowDTO | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionProblem, setActionProblem] = useState<ProblemDetail | null>(null);

  /** Reruns the board's own fetch — shared by error-retry, a successful create, and a successful cancel. */
  const reload = useCallback(() => setReloads((n) => n + 1), []);

  const load = useCallback((signal: AbortSignal) => {
    setLoading(true);
    setProblem(null);
    listMaintenanceWindows({}, signal)
      .then((page) => {
        setRawItems(page.items ?? []);
        setCurrentPageIndex(1);
      })
      .catch((err: unknown) => {
        if (signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load maintenance windows', status: 0, detail: String(err) },
        );
        setRawItems([]);
      })
      .finally(() => {
        if (!signal.aborted) setLoading(false);
      });
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    load(ctrl.signal);
    return () => ctrl.abort();
  }, [reloads, load]);

  const runCancel = useCallback(async () => {
    if (!cancelTarget) return;
    setBusy(true);
    setActionProblem(null);
    try {
      await deleteMaintenanceWindow(cancelTarget.window_id);
      setCancelTarget(null);
      reload();
    } catch (err) {
      setCancelTarget(null);
      setActionProblem(err instanceof ApiError ? err.problem : { title: 'Cancel failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [cancelTarget, reload]);

  const columns = useMemo<TableProps.ColumnDefinition<MaintenanceWindowDTO>[]>(
    () => [
      {
        id: 'asset',
        header: 'Asset',
        isRowHeader: true,
        sortingComparator: (a, b) => a.asset_id.localeCompare(b.asset_id),
        cell: (win) => (
          <div>
            <Box fontWeight="bold">{win.asset_id}</Box>
            <Box fontSize="body-s" color="text-body-secondary">
              {win.window_id}
            </Box>
          </div>
        ),
      },
      {
        id: 'window',
        header: 'Window',
        sortingComparator: (a, b) => new Date(a.starts_at).getTime() - new Date(b.starts_at).getTime(),
        cell: (win) => (
          <span>
            {new Date(win.starts_at).toLocaleString()} → {new Date(win.ends_at).toLocaleString()}
          </span>
        ),
      },
      {
        id: 'reason',
        header: 'Reason',
        sortingComparator: (a, b) => a.reason.localeCompare(b.reason),
        cell: (win) => win.reason || <Box color="text-body-secondary">—</Box>,
      },
      {
        id: 'status',
        header: 'Status',
        sortingComparator: (a, b) => STATUS_RANK[computeMaintenanceWindowStatus(a)] - STATUS_RANK[computeMaintenanceWindowStatus(b)],
        cell: (win) => {
          const status = computeMaintenanceWindowStatus(win);
          return <StatusIndicator type={STATUS_TYPE[status]}>{humanise(status === 'active' ? 'active now' : status)}</StatusIndicator>;
        },
      },
      {
        id: 'suppressed_count',
        header: 'Suppressed',
        sortingComparator: (a, b) => a.suppressed_count - b.suppressed_count,
        cell: (win) => win.suppressed_count,
      },
      {
        id: 'actions',
        header: 'Actions',
        cell: (win) => (
          <Button disabled={busy} onClick={() => setCancelTarget(win)}>
            Cancel
          </Button>
        ),
      },
    ],
    [busy],
  );

  // Tracked by id, not by column object reference: `columns` is recomputed
  // whenever `busy` changes (the actions column closes over it), which would
  // otherwise leave `sortState` holding a stale column/comparator instance
  // from a prior render — Cloudscape's Table warns when the sortingColumn it
  // is given cannot be found (by reference) among the current columns.
  const [sortState, setSortState] = useState<{ columnId: string; descending: boolean }>(
    () => ({ columnId: 'window', descending: false }), // soonest starting first — the default read order.
  );
  const sortingColumn = columns.find((c) => c.id === sortState.columnId) ?? columns[0];

  const filtered = useMemo(
    () => (statusFilter ? rawItems.filter((w) => computeMaintenanceWindowStatus(w) === statusFilter) : rawItems),
    [rawItems, statusFilter],
  );

  const sorted = useMemo(() => {
    const cmp = sortingColumn.sortingComparator ?? (() => 0);
    const dir = sortState.descending ? -1 : 1;
    return [...filtered].sort((a, b) => cmp(a, b) * dir);
  }, [filtered, sortingColumn, sortState.descending]);

  const pagesCount = Math.max(1, Math.ceil(sorted.length / PAGE_SIZE));
  const pageItems = sorted.slice((currentPageIndex - 1) * PAGE_SIZE, currentPageIndex * PAGE_SIZE);

  const clearFilter = () => setStatusFilter('');
  const hasFilter = Boolean(statusFilter);
  const selectedStatus = STATUS_OPTIONS.find((o) => o.value === statusFilter) ?? STATUS_OPTIONS[0];

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Maintenance windows for this tenant. While one is active, its asset's firings are suppressed rather than paged."
        counter={`(${rawItems.length})`}
        actions={<Button onClick={() => setCreateOpen(true)}>Schedule maintenance</Button>}
      >
        Maintenance
      </Header>

      {actionProblem && (
        <div role="alert">
          <Alert type="error" header="Action failed" dismissible onDismiss={() => setActionProblem(null)}>
            {actionProblem.detail ?? `The server returned ${actionProblem.status}.`}
          </Alert>
        </div>
      )}

      <SpaceBetween direction="horizontal" size="s" alignItems="center">
        <Select
          selectedOption={selectedStatus}
          onChange={({ detail }) => setStatusFilter((detail.selectedOption.value ?? '') as MaintenanceWindowStatus | '')}
          options={STATUS_OPTIONS}
          ariaLabel="Filter by status"
        />
        <Box color="text-body-secondary" fontSize="body-s">
          {filtered.length} of {rawItems.length} window{rawItems.length === 1 ? '' : 's'}
        </Box>
      </SpaceBetween>

      {rawItems.length >= MAINTENANCE_WINDOW_LIST_CAP && (
        <Box color="text-status-warning" fontSize="body-s">
          Showing the first {MAINTENANCE_WINDOW_LIST_CAP} maintenance windows for this tenant — narrow further with a
          future server-side filter if this tenant has more.
        </Box>
      )}

      {problem && <ErrorState problem={problem} onRetry={reload} />}

      {!problem && (
        <Table
          items={pageItems}
          columnDefinitions={columns}
          trackBy="window_id"
          loading={loading}
          loadingText="Loading maintenance windows"
          variant="container"
          sortingColumn={sortingColumn}
          sortingDescending={sortState.descending}
          onSortingChange={({ detail }) =>
            setSortState({
              columnId: String((detail.sortingColumn as TableProps.ColumnDefinition<MaintenanceWindowDTO>).id),
              descending: Boolean(detail.isDescending),
            })
          }
          ariaLabels={{ tableLabel: 'Maintenance windows' }}
          empty={<MaintenanceWindowsEmpty hasFilter={hasFilter} onClear={clearFilter} />}
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
        <CreateMaintenanceWindowModal
          onClose={() => setCreateOpen(false)}
          onCreated={() => {
            setCreateOpen(false);
            reload();
          }}
        />
      )}

      {cancelTarget && (
        <Modal
          visible
          header={`Cancel maintenance for ${cancelTarget.asset_id}?`}
          onDismiss={() => {
            if (!busy) setCancelTarget(null);
          }}
          closeAriaLabel="Dismiss"
          footer={
            <Box float="right">
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="link" onClick={() => setCancelTarget(null)} disabled={busy}>
                  Keep it
                </Button>
                <Button variant="primary" loading={busy} onClick={() => void runCancel()}>
                  Cancel window
                </Button>
              </SpaceBetween>
            </Box>
          }
        >
          <Box>
            This permanently withdraws the window — while it is gone, this asset's firings are no longer suppressed,
            and this cannot be undone. Unlike a rule edit, there is no optimistic-lock check on cancel (the endpoint
            takes no row_version).
          </Box>
        </Modal>
      )}
    </SpaceBetween>
  );
}
