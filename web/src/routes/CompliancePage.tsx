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
import { ComplianceControlDetailPanel } from '../components/ComplianceControlDetail';
import { ErrorState } from '../components/States';
import { COMPLIANCE_CONTROL_STATUS_RANK, COMPLIANCE_CONTROL_STATUS_TYPE } from '../compliancePresentation';
import { humanise } from '../incidentPresentation';
import {
  COMPLIANCE_CONTROL_LIST_CAP,
  COMPLIANCE_CONTROL_STATUSES,
  complianceControlFrameworkError,
  complianceControlRefError,
  createComplianceControl,
  listComplianceControls,
} from '../complianceControls';
import type { ComplianceControlDTO, ComplianceControlStatus } from '../complianceControls';

/** Client-side page size over the already-fetched set (see complianceControls.ts' COMPLIANCE_CONTROL_LIST_CAP). */
const PAGE_SIZE = 20;

const STATUS_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All statuses' },
  ...COMPLIANCE_CONTROL_STATUSES.map((s) => ({ value: s, label: humanise(s) })),
];

function ControlsEmpty({
  hasFilter,
  onClear,
}: {
  hasFilter: boolean;
  onClear: () => void;
}) {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No compliance controls{hasFilter ? ' match these filters' : ''}</b>
        <Box variant="p" color="text-body-secondary">
          {hasFilter
            ? 'Try widening the framework or status filter.'
            : 'No compliance control has been registered for this tenant yet.'}
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
        <b>You need tenant-admin permission to manage compliance controls.</b>
        <Box variant="p" color="text-body-secondary">
          Your account does not currently hold the permission OneOps&apos; compliance-control endpoints require. Ask
          your tenant administrator to grant it.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

/**
 * "Create control" (E-SEC-UI.3): framework/control_ref FIXED at creation
 * (immutable — domain.ComplianceControl's own doc comment), title required,
 * description optional. Client-validated against `domain.ComplianceControl.
 * Validate`'s own bounds before submit, mirroring `CreateSecurityRuleModal`'s
 * identical discipline.
 */
function CreateComplianceControlModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (control: ComplianceControlDTO) => void;
}) {
  const [framework, setFramework] = useState('');
  const [controlRef, setControlRef] = useState('');
  const [title, setTitle] = useState('');
  const [description, setDescription] = useState('');
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const frameworkError = complianceControlFrameworkError(framework);
  const controlRefError = complianceControlRefError(controlRef);
  const trimmedFramework = framework.trim();
  const trimmedControlRef = controlRef.trim();
  const trimmedTitle = title.trim();
  const canSubmit =
    trimmedFramework.length > 0 &&
    !frameworkError &&
    trimmedControlRef.length > 0 &&
    !controlRefError &&
    trimmedTitle.length > 0 &&
    !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      const created = await createComplianceControl({
        framework: trimmedFramework,
        control_ref: trimmedControlRef,
        title: trimmedTitle,
        description: description.trim() || undefined,
      });
      onCreated(created);
    } catch (err) {
      setProblem(err instanceof ApiError ? err.problem : { title: 'Create failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [canSubmit, trimmedFramework, trimmedControlRef, trimmedTitle, description, onCreated]);

  return (
    <Modal
      visible
      header="Register a compliance control"
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
              <Alert type="error" header="Could not register the compliance control">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <FormField label="Framework" description="e.g. SOC2, ISO27001, PCI-DSS — fixed once created" errorText={frameworkError}>
            <Input value={framework} onChange={({ detail }) => setFramework(detail.value)} disabled={busy} ariaLabel="Framework" placeholder="Required" />
          </FormField>
          <FormField label="Control ref" description="e.g. CC6.1 — fixed once created" errorText={controlRefError}>
            <Input
              value={controlRef}
              onChange={({ detail }) => setControlRef(detail.value)}
              disabled={busy}
              ariaLabel="Control ref"
              placeholder="Required"
            />
          </FormField>
          <FormField label="Title" errorText={title.length > 0 && trimmedTitle.length === 0 ? 'Title is required.' : undefined}>
            <Input value={title} onChange={({ detail }) => setTitle(detail.value)} disabled={busy} ariaLabel="Title" placeholder="Required" />
          </FormField>
          <FormField label="Description" description="Optional">
            <Textarea value={description} onChange={({ detail }) => setDescription(detail.value)} disabled={busy} ariaLabel="Description" />
          </FormField>
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

/**
 * Compliance (E-SEC-UI.3): the Security console section's compliance-control
 * screen, over the E8.4b compliance-control endpoints. Lists the tenant's
 * controls (filter by framework/status), and drills into one control's
 * detail — its legal-only implementation-lifecycle transitions, title/
 * description Edit, and its append-only evidence trail — through the
 * shell's `SplitPanel`, mirroring `DetectionRulesPage`'s exact board/create/
 * detail shape (ADR-NOC-004, ADR-ACT-001, ADR-SOC-009).
 */
export function CompliancePage() {
  const { openSplitPanel } = useOutletContext<ShellSplitPanelContext>();

  const [frameworkFilter, setFrameworkFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState<ComplianceControlStatus | ''>('');
  const [rawItems, setRawItems] = useState<ComplianceControlDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);
  const [currentPageIndex, setCurrentPageIndex] = useState(1);
  const [createOpen, setCreateOpen] = useState(false);

  const reload = useCallback(() => setReloads((n) => n + 1), []);

  const openControl = useCallback(
    (control: ComplianceControlDTO) => {
      openSplitPanel(`${control.framework} ${control.control_ref}`, <ComplianceControlDetailPanel controlId={control.control_id} onChanged={reload} />);
    },
    [openSplitPanel, reload],
  );

  const columns = useMemo<TableProps.ColumnDefinition<ComplianceControlDTO>[]>(
    () => [
      {
        id: 'framework',
        header: 'Framework',
        sortingComparator: (a, b) => a.framework.localeCompare(b.framework),
        cell: (c) => c.framework,
      },
      {
        id: 'control_ref',
        header: 'Control ref',
        sortingComparator: (a, b) => a.control_ref.localeCompare(b.control_ref),
        cell: (c) => (
          <div>
            <Button variant="inline-link" onClick={() => openControl(c)}>
              {c.control_ref}
            </Button>
            <Box fontSize="body-s" color="text-body-secondary">
              {c.control_id}
            </Box>
          </div>
        ),
      },
      {
        id: 'title',
        header: 'Title',
        isRowHeader: true,
        sortingComparator: (a, b) => a.title.localeCompare(b.title),
        cell: (c) => c.title,
      },
      {
        id: 'status',
        header: 'Status',
        sortingComparator: (a, b) => COMPLIANCE_CONTROL_STATUS_RANK[a.status] - COMPLIANCE_CONTROL_STATUS_RANK[b.status],
        cell: (c) => <StatusIndicator type={COMPLIANCE_CONTROL_STATUS_TYPE[c.status]}>{humanise(c.status)}</StatusIndicator>,
      },
    ],
    [openControl],
  );

  const [sortState, setSortState] = useState<{ column: TableProps.ColumnDefinition<ComplianceControlDTO>; descending: boolean }>(
    () => ({ column: columns[3], descending: false }), // status — the default read order.
  );

  const load = useCallback((framework: string, status: ComplianceControlStatus | '', signal: AbortSignal) => {
    setLoading(true);
    setProblem(null);
    listComplianceControls({ framework, status }, signal)
      .then((page) => {
        setRawItems(page.items ?? []);
        setCurrentPageIndex(1);
      })
      .catch((err: unknown) => {
        if (signal.aborted) return;
        setProblem(err instanceof ApiError ? err.problem : { title: 'Could not load compliance controls', status: 0, detail: String(err) });
        setRawItems([]);
      })
      .finally(() => {
        if (!signal.aborted) setLoading(false);
      });
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    load(frameworkFilter, statusFilter, ctrl.signal);
    return () => ctrl.abort();
  }, [frameworkFilter, statusFilter, reloads, load]);

  const sorted = useMemo(() => {
    const cmp = sortState.column.sortingComparator ?? (() => 0);
    const dir = sortState.descending ? -1 : 1;
    return [...rawItems].sort((a, b) => cmp(a, b) * dir);
  }, [rawItems, sortState]);

  const pagesCount = Math.max(1, Math.ceil(sorted.length / PAGE_SIZE));
  const pageItems = sorted.slice((currentPageIndex - 1) * PAGE_SIZE, currentPageIndex * PAGE_SIZE);

  const clearFilters = () => {
    setFrameworkFilter('');
    setStatusFilter('');
  };
  const hasFilter = Boolean(frameworkFilter || statusFilter);
  const selectedStatus = STATUS_OPTIONS.find((o) => o.value === statusFilter) ?? STATUS_OPTIONS[0];
  const forbidden = problem !== null && problem.status === 403;

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Compliance controls tracked for this tenant, their implementation status, and their filed evidence."
        counter={!forbidden ? `(${rawItems.length})` : undefined}
        actions={<Button onClick={() => setCreateOpen(true)}>Register control</Button>}
      >
        Compliance
      </Header>

      {!forbidden && (
        <SpaceBetween direction="horizontal" size="s" alignItems="center">
          <Input
            value={frameworkFilter}
            onChange={({ detail }) => setFrameworkFilter(detail.value)}
            placeholder="Filter by framework, e.g. SOC2"
            ariaLabel="Filter by framework"
          />
          <Select
            selectedOption={selectedStatus}
            onChange={({ detail }) => setStatusFilter((detail.selectedOption.value ?? '') as ComplianceControlStatus | '')}
            options={STATUS_OPTIONS}
            ariaLabel="Filter by status"
          />
          <Box color="text-body-secondary" fontSize="body-s">
            {rawItems.length} control{rawItems.length === 1 ? '' : 's'}
          </Box>
        </SpaceBetween>
      )}

      {!forbidden && rawItems.length >= COMPLIANCE_CONTROL_LIST_CAP && (
        <Box color="text-status-warning" fontSize="body-s">
          Showing the first {COMPLIANCE_CONTROL_LIST_CAP} compliance controls for this tenant — narrow the filters for
          a complete view.
        </Box>
      )}

      {forbidden && <PermissionNeeded />}

      {!forbidden && problem && <ErrorState problem={problem} onRetry={reload} />}

      {!forbidden && !problem && (
        <Table
          items={pageItems}
          columnDefinitions={columns}
          trackBy="control_id"
          loading={loading}
          loadingText="Loading compliance controls"
          variant="container"
          sortingColumn={sortState.column}
          sortingDescending={sortState.descending}
          onSortingChange={({ detail }) =>
            setSortState({
              column: detail.sortingColumn as TableProps.ColumnDefinition<ComplianceControlDTO>,
              descending: Boolean(detail.isDescending),
            })
          }
          ariaLabels={{ tableLabel: 'Compliance controls' }}
          empty={<ControlsEmpty hasFilter={hasFilter} onClear={clearFilters} />}
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
        <CreateComplianceControlModal
          onClose={() => setCreateOpen(false)}
          onCreated={(created) => {
            setCreateOpen(false);
            reload();
            openControl(created);
          }}
        />
      )}
    </SpaceBetween>
  );
}
