import { useCallback, useEffect, useMemo, useState } from 'react';
import { useOutletContext } from 'react-router-dom';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import Header from '@cloudscape-design/components/header';
import Modal from '@cloudscape-design/components/modal';
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
import { RiskDetailPanel } from '../components/RiskDetail';
import { RiskFormFields } from '../components/RiskForm';
import type { RiskFormValues } from '../components/RiskForm';
import { ErrorState } from '../components/States';
import { humanise } from '../incidentPresentation';
import {
  RISK_IMPACT_RANK,
  RISK_LIKELIHOOD_RANK,
  RISK_SEVERITY_BAND_RANK,
  RISK_SEVERITY_BAND_TYPE,
  RISK_STATUS_RANK,
  RISK_STATUS_TYPE,
} from '../riskPresentation';
import { createRisk, getRiskRegister, listRisks, riskCategoryError, RISK_LIST_CAP, RISK_STATUSES } from '../risks';
import type { CreateRiskInput, RiskDTO, RiskRegisterEntryDTO, RiskStatus } from '../risks';

/** Client-side page size over the already-fetched set (see risks.ts' RISK_LIST_CAP). */
const PAGE_SIZE = 20;

type ViewMode = 'list' | 'register';

const VIEW_OPTIONS: SegmentedControlProps.Option[] = [
  { id: 'list', text: 'All risks' },
  { id: 'register', text: 'Register' },
];

const STATUS_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All statuses' },
  ...RISK_STATUSES.map((s) => ({ value: s, label: humanise(s) })),
];

/** One table row, normalised across both endpoints this board reads — mirrors VulnerabilitiesPage's FindingRow. `list` rows carry no score/band; `register` rows carry the computed projection. */
interface RiskRow {
  risk: RiskDTO;
  score?: number;
  band?: RiskRegisterEntryDTO['band'];
}

function RisksEmpty({ mode, status, onShowAll }: { mode: ViewMode; status: RiskStatus | ''; onShowAll: () => void }) {
  if (mode === 'register') {
    return (
      <Box textAlign="center" color="inherit" padding="l">
        <SpaceBetween size="s">
          <b>No open risks</b>
          <Box variant="p" color="text-body-secondary">
            Nothing is currently open for this tenant — the register only ranks open (non-closed) risks.
          </Box>
        </SpaceBetween>
      </Box>
    );
  }
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No risks{status ? ` with status "${humanise(status)}"` : ''}</b>
        <Box variant="p" color="text-body-secondary">
          {status ? 'Nothing matches this status right now.' : 'No risk has been registered for this tenant yet.'}
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
        <b>You need tenant-admin permission to manage the risk register.</b>
        <Box variant="p" color="text-body-secondary">
          Your account does not currently hold the permission OneOps&apos; risk-register endpoints require. Ask your
          tenant administrator to grant it.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

/** "Create risk" (E-SEC-UI.3): title/likelihood/impact required, everything else optional — POSTs to `createRisk`, always minted `open`. */
function CreateRiskModal({ onClose, onCreated }: { onClose: () => void; onCreated: (risk: RiskDTO) => void }) {
  const [values, setValues] = useState<RiskFormValues>({
    title: '',
    description: '',
    category: '',
    likelihood: 'possible',
    impact: 'moderate',
    treatment: '',
    assetId: '',
  });
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const trimmedTitle = values.title.trim();
  const canSubmit = trimmedTitle.length > 0 && !riskCategoryError(values.category) && !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      const input: CreateRiskInput = {
        title: trimmedTitle,
        description: values.description.trim() || undefined,
        category: values.category.trim() || undefined,
        likelihood: values.likelihood,
        impact: values.impact,
        treatment: values.treatment || undefined,
        asset_id: values.assetId.trim() || undefined,
      };
      const created = await createRisk(input);
      onCreated(created);
    } catch (err) {
      setProblem(err instanceof ApiError ? err.problem : { title: 'Create failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [canSubmit, trimmedTitle, values, onCreated]);

  return (
    <Modal
      visible
      header="Register a new risk"
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
              <Alert type="error" header="Could not register the risk">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <RiskFormFields
            values={values}
            onChange={setValues}
            disabled={busy}
            titleError={values.title.length > 0 && trimmedTitle.length === 0 ? 'Title is required.' : undefined}
          />
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

/**
 * Risk register (E-SEC-UI.3): the Security console section's risk-management
 * screen, over the E8.4a risk-register endpoints. Lists the tenant's risks
 * (filter by status), toggles to the risk-matrix "Register" view (GET
 * .../register, open risks ranked by likelihood x impact score with a
 * severity-band column), and drills into one risk's detail — including its
 * legal-only status transitions and full-field Edit — through the shell's
 * `SplitPanel`, mirroring `VulnerabilitiesPage`'s exact list/prioritized
 * segmented shape (ADR-NOC-004, ADR-ACT-001).
 */
export function RiskRegisterPage() {
  const { openSplitPanel } = useOutletContext<ShellSplitPanelContext>();

  const [mode, setMode] = useState<ViewMode>('list');
  const [statusFilter, setStatusFilter] = useState<RiskStatus | ''>('');
  const [rows, setRows] = useState<RiskRow[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);
  const [currentPageIndex, setCurrentPageIndex] = useState(1);
  const [createOpen, setCreateOpen] = useState(false);
  const [sortState, setSortState] = useState<{ columnId: string; descending: boolean }>({
    columnId: 'status',
    descending: false,
  });

  const reload = useCallback(() => setReloads((n) => n + 1), []);

  const openRisk = useCallback(
    (row: RiskRow) => {
      openSplitPanel(row.risk.title, <RiskDetailPanel riskId={row.risk.risk_id} onChanged={reload} />);
    },
    [openSplitPanel, reload],
  );

  // Switching view mode resets paging and picks that mode's own default sort
  // column — mirrors VulnerabilitiesPage's identical mode-switch effect.
  useEffect(() => {
    setCurrentPageIndex(1);
    // `descending: false` in both cases — the `score`/`status` comparators
    // below already encode "highest risk first"/"open first" as their own
    // natural ascending order (mirrors VulnerabilitiesPage's identical
    // priority/severity comparator convention), so this is NOT "ascending
    // sort" in the visual sense.
    setSortState({ columnId: mode === 'register' ? 'score' : 'status', descending: false });
  }, [mode]);

  const load = useCallback((m: ViewMode, status: RiskStatus | '', signal: AbortSignal) => {
    setLoading(true);
    setProblem(null);
    const request: Promise<RiskRow[]> =
      m === 'register'
        ? getRiskRegister(RISK_LIST_CAP, signal).then((page) =>
            (page.items ?? []).map((e): RiskRow => ({ risk: e.risk, score: e.score, band: e.band })),
          )
        : listRisks({ status }, signal).then((page) => (page.items ?? []).map((r): RiskRow => ({ risk: r })));
    request
      .then((items) => setRows(items))
      .catch((err: unknown) => {
        if (signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: m === 'register' ? 'Could not load the risk register' : 'Could not load risks', status: 0, detail: String(err) },
        );
        setRows([]);
      })
      .finally(() => {
        if (!signal.aborted) setLoading(false);
      });
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    load(mode, statusFilter, ctrl.signal);
    return () => ctrl.abort();
  }, [mode, statusFilter, reloads, load]);

  const forbidden = problem !== null && problem.status === 403;

  const columns = useMemo<TableProps.ColumnDefinition<RiskRow>[]>(() => {
    const cols: TableProps.ColumnDefinition<RiskRow>[] = [
      {
        id: 'title',
        header: 'Risk',
        isRowHeader: true,
        sortingComparator: (a, b) => a.risk.title.localeCompare(b.risk.title),
        cell: (row) => (
          <div>
            <Button variant="inline-link" onClick={() => openRisk(row)}>
              {row.risk.title}
            </Button>
            <Box fontSize="body-s" color="text-body-secondary">
              {row.risk.risk_id}
            </Box>
          </div>
        ),
      },
      {
        id: 'category',
        header: 'Category',
        sortingComparator: (a, b) => a.risk.category.localeCompare(b.risk.category),
        cell: (row) => row.risk.category || <Box color="text-body-secondary">—</Box>,
      },
      {
        id: 'likelihood',
        header: 'Likelihood',
        sortingComparator: (a, b) => RISK_LIKELIHOOD_RANK[a.risk.likelihood] - RISK_LIKELIHOOD_RANK[b.risk.likelihood],
        cell: (row) => humanise(row.risk.likelihood),
      },
      {
        id: 'impact',
        header: 'Impact',
        sortingComparator: (a, b) => RISK_IMPACT_RANK[a.risk.impact] - RISK_IMPACT_RANK[b.risk.impact],
        cell: (row) => humanise(row.risk.impact),
      },
    ];

    if (mode === 'register') {
      // `row.score`/`row.band` are only ever undefined for one transient
      // render — the same "columns switch before rows re-fetch" frame
      // VulnerabilitiesPage's own `priority` column doc comment explains —
      // rendered as the same em-dash fallback, never a crash.
      cols.push(
        {
          id: 'score',
          header: 'Score',
          sortingComparator: (a, b) => (b.score ?? 0) - (a.score ?? 0),
          cell: (row) => row.score ?? <Box color="text-body-secondary">—</Box>,
        },
        {
          id: 'band',
          header: 'Severity band',
          sortingComparator: (a, b) =>
            (RISK_SEVERITY_BAND_RANK[a.band!] ?? Number.MAX_SAFE_INTEGER) - (RISK_SEVERITY_BAND_RANK[b.band!] ?? Number.MAX_SAFE_INTEGER),
          cell: (row) =>
            row.band ? (
              <StatusIndicator type={RISK_SEVERITY_BAND_TYPE[row.band]}>{humanise(row.band)}</StatusIndicator>
            ) : (
              <Box color="text-body-secondary">—</Box>
            ),
        },
      );
    } else {
      cols.push(
        {
          id: 'status',
          header: 'Status',
          sortingComparator: (a, b) => RISK_STATUS_RANK[a.risk.status] - RISK_STATUS_RANK[b.risk.status],
          cell: (row) => <StatusIndicator type={RISK_STATUS_TYPE[row.risk.status]}>{humanise(row.risk.status)}</StatusIndicator>,
        },
        {
          id: 'treatment',
          header: 'Treatment',
          sortingComparator: (a, b) => (a.risk.treatment ?? '').localeCompare(b.risk.treatment ?? ''),
          cell: (row) => (row.risk.treatment ? humanise(row.risk.treatment) : <Box color="text-body-secondary">—</Box>),
        },
      );
    }

    cols.push({
      id: 'asset',
      header: 'Asset',
      sortingComparator: (a, b) => (a.risk.asset_id ?? '').localeCompare(b.risk.asset_id ?? ''),
      cell: (row) => row.risk.asset_id ?? <Box color="text-body-secondary">Unlinked</Box>,
    });

    return cols;
  }, [mode, openRisk]);

  const sortingColumn = columns.find((c) => c.id === sortState.columnId) ?? columns[0];

  const sorted = useMemo(() => {
    const cmp = sortingColumn.sortingComparator ?? (() => 0);
    const dir = sortState.descending ? -1 : 1;
    return [...rows].sort((a, b) => cmp(a, b) * dir);
  }, [rows, sortingColumn, sortState.descending]);

  const pagesCount = Math.max(1, Math.ceil(sorted.length / PAGE_SIZE));
  const pageItems = sorted.slice((currentPageIndex - 1) * PAGE_SIZE, currentPageIndex * PAGE_SIZE);

  const statusSelected = STATUS_OPTIONS.find((o) => o.value === statusFilter) ?? STATUS_OPTIONS[0];

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="The tenant's risk register: operator-identified risks tracked through review to treatment or closure."
        counter={!forbidden ? `(${rows.length})` : undefined}
        actions={
          <SpaceBetween direction="horizontal" size="xs">
            <SegmentedControl
              selectedId={mode}
              onChange={({ detail }) => {
                // Cleared in the SAME state update as the mode switch —
                // mirrors VulnerabilitiesPage's identical rationale.
                setMode(detail.selectedId as ViewMode);
                setRows([]);
              }}
              options={VIEW_OPTIONS}
              label="View"
            />
            <Button onClick={() => setCreateOpen(true)}>Register risk</Button>
          </SpaceBetween>
        }
      >
        Risk register
      </Header>

      {mode === 'list' && !forbidden && (
        <SpaceBetween direction="horizontal" size="s" alignItems="center">
          <Select
            selectedOption={statusSelected}
            onChange={({ detail }) => setStatusFilter((detail.selectedOption.value ?? '') as RiskStatus | '')}
            options={STATUS_OPTIONS}
            ariaLabel="Filter by status"
          />
        </SpaceBetween>
      )}

      {mode === 'register' && !forbidden && (
        <Box color="text-body-secondary" fontSize="body-s">
          Open risks only, ranked by likelihood × impact (the classic risk matrix, ADR-SOC-008). Ties are broken by
          most recently updated.
        </Box>
      )}

      {rows.length >= RISK_LIST_CAP && (
        <Box color="text-status-warning" fontSize="body-s">
          Showing the first {RISK_LIST_CAP} risks for this filter. Narrow the filters for a complete view.
        </Box>
      )}

      {forbidden && <PermissionNeeded />}

      {!forbidden && problem && <ErrorState problem={problem} onRetry={reload} />}

      {!forbidden && !problem && (
        <Table
          items={pageItems}
          columnDefinitions={columns}
          trackBy={(row) => row.risk.risk_id}
          loading={loading}
          loadingText="Loading risks"
          variant="container"
          sortingColumn={sortingColumn}
          sortingDescending={sortState.descending}
          onSortingChange={({ detail }) =>
            setSortState({
              columnId: (detail.sortingColumn as TableProps.ColumnDefinition<RiskRow>).id ?? sortState.columnId,
              descending: Boolean(detail.isDescending),
            })
          }
          ariaLabels={{ tableLabel: mode === 'register' ? 'Risk register, ranked' : 'Risks' }}
          empty={<RisksEmpty mode={mode} status={statusFilter} onShowAll={() => setStatusFilter('')} />}
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
        <CreateRiskModal
          onClose={() => setCreateOpen(false)}
          onCreated={(created) => {
            setCreateOpen(false);
            reload();
            openRisk({ risk: created });
          }}
        />
      )}
    </SpaceBetween>
  );
}
