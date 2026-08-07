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
import { SecurityRuleDetailPanel } from '../components/SecurityRuleDetail';
import { SecurityRuleConfigFields } from '../components/SecurityRuleForm';
import type { SecurityRuleConfigValues } from '../components/SecurityRuleForm';
import { IncidentDetailPanel } from '../components/IncidentDetail';
import { ErrorState } from '../components/States';
import { SEVERITY_TYPE, humanise } from '../incidentPresentation';
import { OBSERVATION_SEVERITY_TYPE, SECURITY_RULE_STATE_RANK, SECURITY_RULE_STATE_TYPE } from '../securityRulePresentation';
import {
  INCIDENT_SEVERITIES,
  SECURITY_RULE_LIST_CAP,
  SECURITY_RULE_STATES,
  createSecurityRule,
  listSecurityRules,
  securityRuleAssetIdError,
  securityRuleObservationTypeError,
  securityRuleThresholdCountError,
  securityRuleWindowSecondsError,
} from '../securityRules';
import type { IncidentSeverity, SecurityRuleDTO, SecurityRuleState } from '../securityRules';

/** Client-side page size over the already-fetched set (see securityRules.ts' SECURITY_RULE_LIST_CAP). */
const PAGE_SIZE = 20;

const STATE_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All states' },
  ...SECURITY_RULE_STATES.map((s) => ({ value: s, label: humanise(s) })),
];

const SEVERITY_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All severities' },
  ...INCIDENT_SEVERITIES.map((s) => ({ value: s, label: humanise(s) })),
];

/** State first (firing before ok), then incident severity — the board's default read order. */
function byStateThenSeverity(a: SecurityRuleDTO, b: SecurityRuleDTO): number {
  return SECURITY_RULE_STATE_RANK[a.last_state] - SECURITY_RULE_STATE_RANK[b.last_state];
}

function DetectionRulesEmpty({ hasFilter, onClear }: { hasFilter: boolean; onClear: () => void }) {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No detection rules{hasFilter ? ' match these filters' : ''}</b>
        <Box variant="p" color="text-body-secondary">
          {hasFilter
            ? 'Try widening the state or severity filter.'
            : 'No detection rules have been registered for this tenant yet.'}
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
        <b>You need tenant-admin permission to manage detection rules.</b>
        <Box variant="p" color="text-body-secondary">
          Your account does not currently hold the permission OneOps&apos; security-rule administration endpoints
          require. Ask your tenant administrator to grant it.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

/**
 * "Create rule" (E-SEC-UI.2): asset_id and observation_type (fixed at
 * creation, free text — reusing `SecurityRuleConfigFields` for the remaining
 * five fields the create and edit forms share). Client-validated before
 * submit, mirroring `domain.SecurityRule.Validate`'s own bounds
 * (`securityRules.ts`'s validator functions); a bad enum is impossible to
 * submit since every enum field is a `Select` over the real backend value
 * set.
 */
function CreateSecurityRuleModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (rule: SecurityRuleDTO) => void;
}) {
  const [assetId, setAssetId] = useState('');
  const [observationType, setObservationType] = useState('');
  const [config, setConfig] = useState<SecurityRuleConfigValues>({
    minSeverity: 'info',
    windowSeconds: '300',
    thresholdCount: '',
    incidentSeverity: 'low',
    enabled: true,
  });
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const assetIdError = securityRuleAssetIdError(assetId);
  const observationTypeError = securityRuleObservationTypeError(observationType);
  const windowSecondsError = securityRuleWindowSecondsError(config.windowSeconds);
  const thresholdCountError = securityRuleThresholdCountError(config.thresholdCount);

  const trimmedAssetId = assetId.trim();
  const trimmedObservationType = observationType.trim();
  const canSubmit =
    trimmedAssetId.length > 0 &&
    !assetIdError &&
    trimmedObservationType.length > 0 &&
    !observationTypeError &&
    config.windowSeconds.trim().length > 0 &&
    !windowSecondsError &&
    config.thresholdCount.trim().length > 0 &&
    !thresholdCountError &&
    !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      const created = await createSecurityRule({
        asset_id: trimmedAssetId,
        observation_type: trimmedObservationType,
        min_severity: config.minSeverity,
        window_seconds: Number(config.windowSeconds),
        threshold_count: Number(config.thresholdCount),
        incident_severity: config.incidentSeverity,
        enabled: config.enabled,
      });
      onCreated(created);
    } catch (err) {
      setProblem(err instanceof ApiError ? err.problem : { title: 'Create failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [canSubmit, trimmedAssetId, trimmedObservationType, config, onCreated]);

  return (
    <Modal
      visible
      header="Create detection rule"
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
              <Alert type="error" header="Could not create the detection rule">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <FormField label="Asset ID" errorText={assetIdError}>
            <Input value={assetId} onChange={({ detail }) => setAssetId(detail.value)} disabled={busy} ariaLabel="Asset ID" placeholder="Required" />
          </FormField>
          <FormField label="Observation type" description="Lower-case snake_case, e.g. port_scan" errorText={observationTypeError}>
            <Input
              value={observationType}
              onChange={({ detail }) => setObservationType(detail.value)}
              disabled={busy}
              ariaLabel="Observation type"
              placeholder="Required"
            />
          </FormField>
          <SecurityRuleConfigFields
            values={config}
            onChange={setConfig}
            disabled={busy}
            errors={{ windowSeconds: windowSecondsError, thresholdCount: thresholdCountError }}
          />
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

/**
 * The detection-rules board (E-SEC-UI.2): every security_rule for the
 * caller's tenant (bounded at SECURITY_RULE_LIST_CAP), filterable by state
 * and incident severity, sortable, and drilling into a rule's own detail
 * through the shell's `SplitPanel` — the same pattern `AlertsBoardPage`
 * established. "Create rule" from this board, edit/enable-disable/delete
 * from the detail panel it opens, and "Linked incident" (current_incident_id)
 * reuses the exact same drill-down idiom `AlertsBoardPage.openIncident` uses:
 * `openSplitPanel` with `IncidentDetailPanel`, not a new route.
 */
export function DetectionRulesPage() {
  const { openSplitPanel, closeSplitPanel } = useOutletContext<ShellSplitPanelContext>();

  const [stateFilter, setStateFilter] = useState<SecurityRuleState | ''>('');
  const [severityFilter, setSeverityFilter] = useState<IncidentSeverity | ''>('');
  const [rawItems, setRawItems] = useState<SecurityRuleDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);
  const [currentPageIndex, setCurrentPageIndex] = useState(1);
  const [createOpen, setCreateOpen] = useState(false);

  const reload = useCallback(() => setReloads((n) => n + 1), []);

  const openRule = useCallback(
    (rule: SecurityRuleDTO) => {
      openSplitPanel(
        `${rule.asset_id} · ${rule.observation_type}`,
        <SecurityRuleDetailPanel
          ruleId={rule.rule_id}
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

  const openIncident = useCallback(
    (incidentId: string) => {
      openSplitPanel(`Incident ${incidentId}`, <IncidentDetailPanel incidentId={incidentId} />);
    },
    [openSplitPanel],
  );

  const columns = useMemo<TableProps.ColumnDefinition<SecurityRuleDTO>[]>(
    () => [
      {
        id: 'rule',
        header: 'Rule',
        isRowHeader: true,
        sortingComparator: (a, b) => `${a.asset_id}${a.observation_type}`.localeCompare(`${b.asset_id}${b.observation_type}`),
        cell: (rule) => (
          <div>
            <Button variant="inline-link" onClick={() => openRule(rule)}>
              {rule.asset_id} · {rule.observation_type}
            </Button>
            <Box fontSize="body-s" color="text-body-secondary">
              {rule.rule_id}
            </Box>
          </div>
        ),
      },
      {
        id: 'min_severity',
        header: 'Min severity',
        sortingComparator: (a, b) => a.min_severity.localeCompare(b.min_severity),
        cell: (rule) => (
          <StatusIndicator type={OBSERVATION_SEVERITY_TYPE[rule.min_severity]}>{humanise(rule.min_severity)}</StatusIndicator>
        ),
      },
      {
        id: 'threshold',
        header: 'Threshold',
        sortingComparator: (a, b) => a.threshold_count - b.threshold_count,
        cell: (rule) => `${rule.threshold_count} in ${rule.window_seconds}s`,
      },
      {
        id: 'incident_severity',
        header: 'Incident severity',
        sortingComparator: (a, b) => a.incident_severity.localeCompare(b.incident_severity),
        cell: (rule) => <StatusIndicator type={SEVERITY_TYPE[rule.incident_severity]}>{humanise(rule.incident_severity)}</StatusIndicator>,
      },
      {
        id: 'state',
        header: 'State',
        sortingComparator: byStateThenSeverity,
        cell: (rule) => <StatusIndicator type={SECURITY_RULE_STATE_TYPE[rule.last_state]}>{humanise(rule.last_state)}</StatusIndicator>,
      },
      {
        id: 'enabled',
        header: 'Enabled',
        sortingComparator: (a, b) => Number(b.enabled) - Number(a.enabled),
        cell: (rule) => (
          <StatusIndicator type={rule.enabled ? 'success' : 'stopped'}>{rule.enabled ? 'Enabled' : 'Disabled'}</StatusIndicator>
        ),
      },
      {
        id: 'current_incident_id',
        header: 'Linked incident',
        sortingComparator: (a, b) => (a.current_incident_id ?? '').localeCompare(b.current_incident_id ?? ''),
        cell: (rule) =>
          rule.current_incident_id ? (
            <Button variant="inline-link" onClick={() => openIncident(rule.current_incident_id!)}>
              {rule.current_incident_id}
            </Button>
          ) : (
            <Box color="text-body-secondary">—</Box>
          ),
      },
    ],
    [openRule, openIncident],
  );

  const [sortState, setSortState] = useState<{ column: TableProps.ColumnDefinition<SecurityRuleDTO>; descending: boolean }>(
    () => ({ column: columns[4], descending: false }), // state — the default read order.
  );

  const load = useCallback((signal: AbortSignal) => {
    setLoading(true);
    setProblem(null);
    listSecurityRules({}, signal)
      .then((page) => {
        setRawItems(page.items ?? []);
        setCurrentPageIndex(1);
      })
      .catch((err: unknown) => {
        if (signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load detection rules', status: 0, detail: String(err) },
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

  const filtered = useMemo(
    () =>
      rawItems.filter(
        (r) => (!stateFilter || r.last_state === stateFilter) && (!severityFilter || r.incident_severity === severityFilter),
      ),
    [rawItems, stateFilter, severityFilter],
  );

  const sorted = useMemo(() => {
    const cmp = sortState.column.sortingComparator ?? (() => 0);
    const dir = sortState.descending ? -1 : 1;
    return [...filtered].sort((a, b) => cmp(a, b) * dir);
  }, [filtered, sortState]);

  const pagesCount = Math.max(1, Math.ceil(sorted.length / PAGE_SIZE));
  const pageItems = sorted.slice((currentPageIndex - 1) * PAGE_SIZE, currentPageIndex * PAGE_SIZE);

  const clearFilters = () => {
    setStateFilter('');
    setSeverityFilter('');
  };
  const hasFilter = Boolean(stateFilter || severityFilter);
  const selectedState = STATE_OPTIONS.find((o) => o.value === stateFilter) ?? STATE_OPTIONS[0];
  const selectedSeverity = SEVERITY_OPTIONS.find((o) => o.value === severityFilter) ?? SEVERITY_OPTIONS[0];
  const forbidden = problem !== null && problem.status === 403;

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Threshold-over-window detection rules for this tenant. Click a rule for detail."
        counter={!forbidden ? `(${rawItems.length})` : undefined}
        actions={<Button onClick={() => setCreateOpen(true)}>Create rule</Button>}
      >
        Detection rules
      </Header>

      {!forbidden && (
        <SpaceBetween direction="horizontal" size="s" alignItems="center">
          <Select
            selectedOption={selectedState}
            onChange={({ detail }) => setStateFilter((detail.selectedOption.value ?? '') as SecurityRuleState | '')}
            options={STATE_OPTIONS}
            ariaLabel="Filter by state"
          />
          <Select
            selectedOption={selectedSeverity}
            onChange={({ detail }) => setSeverityFilter((detail.selectedOption.value ?? '') as IncidentSeverity | '')}
            options={SEVERITY_OPTIONS}
            ariaLabel="Filter by severity"
          />
          <Box color="text-body-secondary" fontSize="body-s">
            {filtered.length} of {rawItems.length} rule{rawItems.length === 1 ? '' : 's'}
          </Box>
        </SpaceBetween>
      )}

      {!forbidden && rawItems.length >= SECURITY_RULE_LIST_CAP && (
        <Box color="text-status-warning" fontSize="body-s">
          Showing the first {SECURITY_RULE_LIST_CAP} detection rules for this tenant — narrow further with a future
          server-side filter if this tenant has more.
        </Box>
      )}

      {forbidden && <PermissionNeeded />}

      {!forbidden && problem && <ErrorState problem={problem} onRetry={reload} />}

      {!forbidden && !problem && (
        <Table
          items={pageItems}
          columnDefinitions={columns}
          trackBy="rule_id"
          loading={loading}
          loadingText="Loading detection rules"
          variant="container"
          sortingColumn={sortState.column}
          sortingDescending={sortState.descending}
          onSortingChange={({ detail }) =>
            setSortState({
              column: detail.sortingColumn as TableProps.ColumnDefinition<SecurityRuleDTO>,
              descending: Boolean(detail.isDescending),
            })
          }
          ariaLabels={{ tableLabel: 'Detection rules' }}
          empty={<DetectionRulesEmpty hasFilter={hasFilter} onClear={clearFilters} />}
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
        <CreateSecurityRuleModal
          onClose={() => setCreateOpen(false)}
          onCreated={(created) => {
            setCreateOpen(false);
            reload();
            openRule(created);
          }}
        />
      )}
    </SpaceBetween>
  );
}
