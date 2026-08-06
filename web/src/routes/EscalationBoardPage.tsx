import { useCallback, useEffect, useMemo, useState } from 'react';
import { useOutletContext } from 'react-router-dom';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import Header from '@cloudscape-design/components/header';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import type { TableProps } from '@cloudscape-design/components/table';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { EscalationPolicyDetailPanel } from '../components/EscalationPolicyDetail';
import { EscalationPolicyFields } from '../components/EscalationPolicyForm';
import type { EscalationPolicyFieldValues } from '../components/EscalationPolicyForm';
import { ESCALATION_POLICY_LIST_CAP, createEscalationPolicy, listEscalationPolicies } from '../escalation';
import type { EscalationPolicyDTO } from '../escalation';
import { ESCALATION_POLICY_STATUS_TYPE } from '../escalationPresentation';
import { ErrorState } from '../components/States';
import { humanise } from '../incidentPresentation';
import type { ShellSplitPanelContext } from '../Shell';

/**
 * "Create policy" (E-ACT.5): the one field createEscalationPolicyRequest
 * takes — name; status is not collected — a freshly created policy is
 * always "active" server-side (domain.NewEscalationPolicy), there is no
 * caller-chosen initial status to offer, the identical shape
 * CreateOnCallScheduleModal uses for its own always-active create.
 */
function CreateEscalationPolicyModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (p: EscalationPolicyDTO) => void;
}) {
  const [fields, setFields] = useState<EscalationPolicyFieldValues>({ name: '' });
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const trimmedName = fields.name.trim();
  const canSubmit = trimmedName.length > 0 && trimmedName.length <= 200 && !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      const created = await createEscalationPolicy({ name: trimmedName });
      onCreated(created);
    } catch (err) {
      setProblem(err instanceof ApiError ? err.problem : { title: 'Create failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [canSubmit, trimmedName, onCreated]);

  return (
    <Modal
      visible
      header="Create policy"
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
              <Alert type="error" header="Could not create the policy">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <EscalationPolicyFields values={fields} onChange={setFields} disabled={busy} />
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

function EscalationPoliciesEmpty() {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No escalation policies have been created yet.</b>
        <Box variant="p" color="text-body-secondary">
          Create one and add tiers to page an on-call schedule, wait for acknowledgement, then advance.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

/**
 * The escalation board (E-ACT.5): every escalation policy for the caller's
 * tenant (bounded at ESCALATION_POLICY_LIST_CAP), drilling into a policy's
 * own tier-ladder management through the shell's `SplitPanel` — the same
 * `Table` + drill-down pattern `AlertsBoardPage` established (ADR-ACT-002),
 * chosen over `Cards` here because a policy's own board-level fields
 * (name/status) are simple columns, unlike the on-call board's richer
 * per-schedule sections.
 */
export function EscalationBoardPage() {
  const { openSplitPanel } = useOutletContext<ShellSplitPanelContext>();

  const [policies, setPolicies] = useState<EscalationPolicyDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);
  const [createOpen, setCreateOpen] = useState(false);

  const reload = useCallback(() => setReloads((n) => n + 1), []);

  const openPolicy = useCallback(
    (p: EscalationPolicyDTO) => {
      openSplitPanel(
        p.name,
        <EscalationPolicyDetailPanel policyId={p.policy_id} onChanged={reload} />,
      );
    },
    [openSplitPanel, reload],
  );

  const load = useCallback((signal: AbortSignal) => {
    setLoading(true);
    setProblem(null);
    listEscalationPolicies(signal)
      .then((page) => setPolicies(page.items ?? []))
      .catch((err: unknown) => {
        if (signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load escalation policies', status: 0, detail: String(err) },
        );
        setPolicies([]);
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

  const columns = useMemo<TableProps.ColumnDefinition<EscalationPolicyDTO>[]>(
    () => [
      {
        id: 'name',
        header: 'Policy',
        isRowHeader: true,
        sortingComparator: (a, b) => a.name.localeCompare(b.name),
        cell: (p) => (
          <Button variant="inline-link" onClick={() => openPolicy(p)}>
            {p.name}
          </Button>
        ),
      },
      {
        id: 'status',
        header: 'Status',
        sortingComparator: (a, b) => a.status.localeCompare(b.status),
        cell: (p) => <StatusIndicator type={ESCALATION_POLICY_STATUS_TYPE[p.status]}>{humanise(p.status)}</StatusIndicator>,
      },
      {
        id: 'updated',
        header: 'Updated',
        sortingComparator: (a, b) => a.updated_at.localeCompare(b.updated_at),
        cell: (p) => new Date(p.updated_at).toLocaleString(),
      },
      {
        id: 'actions',
        header: 'Actions',
        cell: (p) => <Button onClick={() => openPolicy(p)}>Manage tiers</Button>,
      },
    ],
    [openPolicy],
  );

  const [sortState, setSortState] = useState<{ column: TableProps.ColumnDefinition<EscalationPolicyDTO>; descending: boolean }>(
    () => ({ column: columns[0], descending: false }),
  );

  const sorted = useMemo(() => {
    const cmp = sortState.column.sortingComparator ?? (() => 0);
    const dir = sortState.descending ? -1 : 1;
    return [...policies].sort((a, b) => cmp(a, b) * dir);
  }, [policies, sortState]);

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Escalation policies for this tenant: an ordered ladder of on-call schedules to page, and how long to wait for an acknowledgement at each tier."
        counter={`(${policies.length})`}
        actions={<Button onClick={() => setCreateOpen(true)}>Create policy</Button>}
      >
        Escalation
      </Header>

      {policies.length >= ESCALATION_POLICY_LIST_CAP && (
        <Box color="text-status-warning" fontSize="body-s">
          Showing the first {ESCALATION_POLICY_LIST_CAP} escalation policies for this tenant.
        </Box>
      )}

      {problem && <ErrorState problem={problem} onRetry={reload} />}

      {!problem && (
        <Table
          items={sorted}
          columnDefinitions={columns}
          trackBy="policy_id"
          loading={loading}
          loadingText="Loading escalation policies"
          variant="container"
          sortingColumn={sortState.column}
          sortingDescending={sortState.descending}
          onSortingChange={({ detail }) =>
            setSortState({
              column: detail.sortingColumn as TableProps.ColumnDefinition<EscalationPolicyDTO>,
              descending: Boolean(detail.isDescending),
            })
          }
          ariaLabels={{ tableLabel: 'Escalation policies' }}
          empty={<EscalationPoliciesEmpty />}
        />
      )}

      {createOpen && (
        <CreateEscalationPolicyModal
          onClose={() => setCreateOpen(false)}
          onCreated={(created) => {
            setCreateOpen(false);
            reload();
            openPolicy(created);
          }}
        />
      )}
    </SpaceBetween>
  );
}
