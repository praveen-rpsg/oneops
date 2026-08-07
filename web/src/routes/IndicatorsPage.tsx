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
import type { ShellSplitPanelContext } from '../Shell';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { IOCConfigFields } from '../components/IOCForm';
import type { IOCConfigValues } from '../components/IOCForm';
import { IOCDetailPanel } from '../components/IOCDetail';
import { ErrorState } from '../components/States';
import { SEVERITY_RANK, SEVERITY_TYPE, humanise } from '../incidentPresentation';
import {
  INCIDENT_SEVERITIES,
  IOC_INDICATOR_TYPES,
  IOC_LIST_CAP,
  createIOC,
  iocDescriptionError,
  iocIndicatorValueError,
  iocSourceError,
  listIOCs,
  normalizeIOCIndicatorValue,
} from '../iocs';
import type { IOCDTO, IOCIndicatorType, IncidentSeverity } from '../iocs';

/** Client-side page size over the already-fetched set (see iocs.ts' IOC_LIST_CAP). */
const PAGE_SIZE = 20;

const TYPE_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All types' },
  ...IOC_INDICATOR_TYPES.map((t) => ({ value: t, label: humanise(t) })),
];

const SEVERITY_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All severities' },
  ...INCIDENT_SEVERITIES.map((s) => ({ value: s, label: humanise(s) })),
];

const CREATE_TYPE_OPTIONS: SelectProps.Option[] = IOC_INDICATOR_TYPES.map((t) => ({ value: t, label: humanise(t) }));

function bySeverity(a: IOCDTO, b: IOCDTO): number {
  return SEVERITY_RANK[a.severity] - SEVERITY_RANK[b.severity];
}

function IndicatorsEmpty({ hasFilter, onClear }: { hasFilter: boolean; onClear: () => void }) {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No indicators{hasFilter ? ' match these filters' : ''}</b>
        <Box variant="p" color="text-body-secondary">
          {hasFilter
            ? 'Try widening the type or severity filter.'
            : 'No indicators of compromise have been added to this tenant’s watchlist yet.'}
        </Box>
        {hasFilter && <Button onClick={onClear}>Clear filters</Button>}
      </SpaceBetween>
    </Box>
  );
}

/** The clean, honest 403: the server's own refusal, never a crash or a raw dump. */
function PermissionNeeded() {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>You need tenant-admin permission to manage indicators of compromise.</b>
        <Box variant="p" color="text-body-secondary">
          Your account does not currently hold the permission OneOps&apos; IOC administration endpoints require. Ask
          your tenant administrator to grant it.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

/**
 * "Add indicator" (E-SEC-UI.2): indicator_type + indicator_value (fixed at
 * creation, collected here — reusing `IOCConfigFields` for the remaining
 * four fields the create and edit forms share). Client-validated before
 * submit, mirroring `domain.IOC.Validate`'s own bounds (`iocs.ts`'s validator
 * functions); a bad enum is impossible to submit since indicator_type and
 * severity are both `Select`s over the real backend value sets. A duplicate
 * (tenant, indicator_type, indicator_value) is rejected by the server with
 * 409 — surfaced inline in this same dialog, the same "stay open, show the
 * problem" contract every create modal in this console follows.
 */
function CreateIOCModal({ onClose, onCreated }: { onClose: () => void; onCreated: (ioc: IOCDTO) => void }) {
  const [indicatorType, setIndicatorType] = useState<IOCIndicatorType>('ip');
  const [indicatorValue, setIndicatorValue] = useState('');
  const [config, setConfig] = useState<IOCConfigValues>({ severity: 'low', enabled: true, description: '', source: '' });
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const indicatorValueError = iocIndicatorValueError(indicatorValue);
  const descriptionError = iocDescriptionError(config.description);
  const sourceError = iocSourceError(config.source);

  const normalizedValue = normalizeIOCIndicatorValue(indicatorType, indicatorValue);
  const canSubmit =
    normalizedValue.length > 0 && !indicatorValueError && !descriptionError && !sourceError && !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      const created = await createIOC({
        indicator_type: indicatorType,
        indicator_value: indicatorValue.trim(),
        severity: config.severity,
        enabled: config.enabled,
        description: config.description || undefined,
        source: config.source || undefined,
      });
      onCreated(created);
    } catch (err) {
      setProblem(err instanceof ApiError ? err.problem : { title: 'Create failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [canSubmit, indicatorType, indicatorValue, config, onCreated]);

  return (
    <Modal
      visible
      header="Add indicator"
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
              Add
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Form>
        <SpaceBetween size="m">
          {problem && (
            <div role="alert">
              <Alert type="error" header={problem.status === 409 ? 'Already on the watchlist' : 'Could not add the indicator'}>
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <FormField label="Indicator type">
            <Select
              selectedOption={CREATE_TYPE_OPTIONS.find((o) => o.value === indicatorType) ?? CREATE_TYPE_OPTIONS[0]}
              onChange={({ detail }) => setIndicatorType((detail.selectedOption.value ?? 'ip') as IOCIndicatorType)}
              options={CREATE_TYPE_OPTIONS}
              disabled={busy}
              ariaLabel="Indicator type"
            />
          </FormField>
          <FormField
            label="Indicator value"
            description="Normalized before storage: trimmed, and lower-cased for domain/url/email"
            errorText={indicatorValueError}
          >
            <Input value={indicatorValue} onChange={({ detail }) => setIndicatorValue(detail.value)} disabled={busy} ariaLabel="Indicator value" placeholder="Required" />
          </FormField>
          <IOCConfigFields
            values={config}
            onChange={setConfig}
            disabled={busy}
            errors={{ description: descriptionError, source: sourceError }}
          />
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

/**
 * The indicators board (E-SEC-UI.2): every ioc for the caller's tenant
 * (bounded at IOC_LIST_CAP), filterable by indicator type and severity,
 * sortable, and drilling into an entry's own detail through the shell's
 * `SplitPanel` — the same pattern `AlertsBoardPage`/`DetectionRulesPage`
 * establish. "Add indicator" from this board; edit/enable-disable/delete
 * from the detail panel it opens.
 */
export function IndicatorsPage() {
  const { openSplitPanel, closeSplitPanel } = useOutletContext<ShellSplitPanelContext>();

  const [typeFilter, setTypeFilter] = useState<IOCIndicatorType | ''>('');
  const [severityFilter, setSeverityFilter] = useState<IncidentSeverity | ''>('');
  const [rawItems, setRawItems] = useState<IOCDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);
  const [currentPageIndex, setCurrentPageIndex] = useState(1);
  const [createOpen, setCreateOpen] = useState(false);

  const reload = useCallback(() => setReloads((n) => n + 1), []);

  const openEntry = useCallback(
    (ioc: IOCDTO) => {
      openSplitPanel(
        ioc.indicator_value,
        <IOCDetailPanel
          iocId={ioc.ioc_id}
          onChanged={reload}
          onDeleted={() => {
            closeSplitPanel();
            reload();
          }}
        />,
      );
    },
    [openSplitPanel, closeSplitPanel, reload],
  );

  const columns = useMemo<TableProps.ColumnDefinition<IOCDTO>[]>(
    () => [
      {
        id: 'indicator_value',
        header: 'Indicator',
        isRowHeader: true,
        sortingComparator: (a, b) => a.indicator_value.localeCompare(b.indicator_value),
        cell: (ioc) => (
          <div>
            <Button variant="inline-link" onClick={() => openEntry(ioc)}>
              {ioc.indicator_value}
            </Button>
            <Box fontSize="body-s" color="text-body-secondary">
              {ioc.ioc_id}
            </Box>
          </div>
        ),
      },
      {
        id: 'indicator_type',
        header: 'Type',
        sortingComparator: (a, b) => a.indicator_type.localeCompare(b.indicator_type),
        cell: (ioc) => humanise(ioc.indicator_type),
      },
      {
        id: 'severity',
        header: 'Severity',
        sortingComparator: bySeverity,
        cell: (ioc) => <StatusIndicator type={SEVERITY_TYPE[ioc.severity]}>{humanise(ioc.severity)}</StatusIndicator>,
      },
      {
        id: 'enabled',
        header: 'Enabled',
        sortingComparator: (a, b) => Number(b.enabled) - Number(a.enabled),
        cell: (ioc) => (
          <StatusIndicator type={ioc.enabled ? 'success' : 'stopped'}>{ioc.enabled ? 'Enabled' : 'Disabled'}</StatusIndicator>
        ),
      },
      {
        id: 'source',
        header: 'Source',
        sortingComparator: (a, b) => a.source.localeCompare(b.source),
        cell: (ioc) => ioc.source || <Box color="text-body-secondary">—</Box>,
      },
      {
        id: 'description',
        header: 'Description',
        cell: (ioc) => ioc.description || <Box color="text-body-secondary">—</Box>,
      },
    ],
    [openEntry],
  );

  const [sortState, setSortState] = useState<{ column: TableProps.ColumnDefinition<IOCDTO>; descending: boolean }>(
    () => ({ column: columns[2], descending: false }), // severity — the default read order.
  );

  const load = useCallback((signal: AbortSignal) => {
    setLoading(true);
    setProblem(null);
    listIOCs({}, signal)
      .then((page) => {
        setRawItems(page.items ?? []);
        setCurrentPageIndex(1);
      })
      .catch((err: unknown) => {
        if (signal.aborted) return;
        setProblem(err instanceof ApiError ? err.problem : { title: 'Could not load indicators', status: 0, detail: String(err) });
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

  const filtered = useMemo(
    () =>
      rawItems.filter(
        (i) => (!typeFilter || i.indicator_type === typeFilter) && (!severityFilter || i.severity === severityFilter),
      ),
    [rawItems, typeFilter, severityFilter],
  );

  const sorted = useMemo(() => {
    const cmp = sortState.column.sortingComparator ?? (() => 0);
    const dir = sortState.descending ? -1 : 1;
    return [...filtered].sort((a, b) => cmp(a, b) * dir);
  }, [filtered, sortState]);

  const pagesCount = Math.max(1, Math.ceil(sorted.length / PAGE_SIZE));
  const pageItems = sorted.slice((currentPageIndex - 1) * PAGE_SIZE, currentPageIndex * PAGE_SIZE);

  const clearFilters = () => {
    setTypeFilter('');
    setSeverityFilter('');
  };
  const hasFilter = Boolean(typeFilter || severityFilter);
  const selectedType = TYPE_OPTIONS.find((o) => o.value === typeFilter) ?? TYPE_OPTIONS[0];
  const selectedSeverity = SEVERITY_OPTIONS.find((o) => o.value === severityFilter) ?? SEVERITY_OPTIONS[0];
  const forbidden = problem !== null && problem.status === 403;

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Curated indicators of compromise for this tenant. Click an indicator for detail."
        counter={!forbidden ? `(${rawItems.length})` : undefined}
        actions={<Button onClick={() => setCreateOpen(true)}>Add indicator</Button>}
      >
        Indicators
      </Header>

      {!forbidden && (
        <SpaceBetween direction="horizontal" size="s" alignItems="center">
          <Select
            selectedOption={selectedType}
            onChange={({ detail }) => setTypeFilter((detail.selectedOption.value ?? '') as IOCIndicatorType | '')}
            options={TYPE_OPTIONS}
            ariaLabel="Filter by type"
          />
          <Select
            selectedOption={selectedSeverity}
            onChange={({ detail }) => setSeverityFilter((detail.selectedOption.value ?? '') as IncidentSeverity | '')}
            options={SEVERITY_OPTIONS}
            ariaLabel="Filter by severity"
          />
          <Box color="text-body-secondary" fontSize="body-s">
            {filtered.length} of {rawItems.length} indicator{rawItems.length === 1 ? '' : 's'}
          </Box>
        </SpaceBetween>
      )}

      {!forbidden && rawItems.length >= IOC_LIST_CAP && (
        <Box color="text-status-warning" fontSize="body-s">
          Showing the first {IOC_LIST_CAP} indicators for this tenant — narrow further with a future server-side
          filter if this tenant has more.
        </Box>
      )}

      {forbidden && <PermissionNeeded />}

      {!forbidden && problem && <ErrorState problem={problem} onRetry={reload} />}

      {!forbidden && !problem && (
        <Table
          items={pageItems}
          columnDefinitions={columns}
          trackBy="ioc_id"
          loading={loading}
          loadingText="Loading indicators"
          variant="container"
          sortingColumn={sortState.column}
          sortingDescending={sortState.descending}
          onSortingChange={({ detail }) =>
            setSortState({ column: detail.sortingColumn as TableProps.ColumnDefinition<IOCDTO>, descending: Boolean(detail.isDescending) })
          }
          ariaLabels={{ tableLabel: 'Indicators' }}
          empty={<IndicatorsEmpty hasFilter={hasFilter} onClear={clearFilters} />}
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
        <CreateIOCModal
          onClose={() => setCreateOpen(false)}
          onCreated={(created) => {
            setCreateOpen(false);
            reload();
            openEntry(created);
          }}
        />
      )}
    </SpaceBetween>
  );
}
