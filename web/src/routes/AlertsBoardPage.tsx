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
import { ALERT_SEVERITY_RANK, ALERT_SEVERITY_TYPE, ALERT_STATE_RANK, ALERT_STATE_TYPE, COMPARATOR_SYMBOL } from '../alertPresentation';
import {
  ALERT_RULE_LIST_CAP,
  ALERT_RULE_STATES,
  ALERT_SEVERITIES,
  alertRuleAssetIdError,
  alertRuleFlapDwellError,
  alertRuleForDurationError,
  alertRuleMetricError,
  alertRuleThresholdError,
  createAlertRule,
  listAlertRules,
} from '../alertRules';
import type { AlertRuleDTO, AlertRuleState, AlertSeverity } from '../alertRules';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { AlertRuleDetailPanel } from '../components/AlertRuleDetail';
import { AlertRuleConfigFields } from '../components/AlertRuleForm';
import type { AlertRuleConfigValues } from '../components/AlertRuleForm';
import { IncidentDetailPanel } from '../components/IncidentDetail';
import { ErrorState } from '../components/States';
import { humanise } from '../incidentPresentation';

/** Client-side page size over the already-fetched set (see alertRules.ts' ALERT_RULE_LIST_CAP). */
const PAGE_SIZE = 20;

const STATE_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All states' },
  ...ALERT_RULE_STATES.map((s) => ({ value: s, label: humanise(s) })),
];

const SEVERITY_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All severities' },
  ...ALERT_SEVERITIES.map((s) => ({ value: s, label: humanise(s) })),
];

/** Severity first, then state (firing before ok) — the board's default read order. */
function bySeverityThenState(a: AlertRuleDTO, b: AlertRuleDTO): number {
  return ALERT_SEVERITY_RANK[a.severity] - ALERT_SEVERITY_RANK[b.severity]
    || ALERT_STATE_RANK[a.last_state] - ALERT_STATE_RANK[b.last_state];
}

function AlertRulesEmpty({
  hasFilter,
  onClear,
}: {
  hasFilter: boolean;
  onClear: () => void;
}) {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No alert rules{hasFilter ? ' match these filters' : ''}</b>
        <Box variant="p" color="text-body-secondary">
          {hasFilter
            ? 'Try widening the state or severity filter.'
            : 'No alert rules have been registered for this tenant yet.'}
        </Box>
        {hasFilter && <Button onClick={onClear}>Clear filters</Button>}
      </SpaceBetween>
    </Box>
  );
}

/**
 * "Create rule" (ADR-ACT-002): asset_id and metric (fixed at creation, free
 * text — reusing `AlertRuleConfigFields` for the remaining seven fields the
 * create and edit forms share). Client-validated before submit, mirroring
 * `domain.AlertRule.Validate`'s own bounds (`alertRules.ts`'s validator
 * functions); a bad enum is impossible to submit since every enum field is a
 * `Select` over the real backend value set.
 */
function CreateAlertRuleModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (rule: AlertRuleDTO) => void;
}) {
  const [assetId, setAssetId] = useState('');
  const [metric, setMetric] = useState('');
  const [config, setConfig] = useState<AlertRuleConfigValues>({
    comparator: 'gt',
    threshold: '',
    forDurationSeconds: '300',
    severity: 'warning',
    symptomClass: 'unspecified',
    flapDwellSeconds: '',
    enabled: true,
  });
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const assetIdError = alertRuleAssetIdError(assetId);
  const metricError = alertRuleMetricError(metric);
  const thresholdError = alertRuleThresholdError(config.threshold);
  const forDurationError = alertRuleForDurationError(config.forDurationSeconds);
  const flapDwellError = alertRuleFlapDwellError(config.flapDwellSeconds);

  const trimmedAssetId = assetId.trim();
  const trimmedMetric = metric.trim();
  const canSubmit =
    trimmedAssetId.length > 0 &&
    !assetIdError &&
    trimmedMetric.length > 0 &&
    !metricError &&
    config.threshold.trim().length > 0 &&
    !thresholdError &&
    config.forDurationSeconds.trim().length > 0 &&
    !forDurationError &&
    !flapDwellError &&
    !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      const created = await createAlertRule({
        asset_id: trimmedAssetId,
        metric: trimmedMetric,
        comparator: config.comparator,
        threshold: Number(config.threshold),
        for_duration_seconds: Number(config.forDurationSeconds),
        severity: config.severity,
        symptom_class: config.symptomClass,
        enabled: config.enabled,
        flap_dwell_seconds: config.flapDwellSeconds.trim() === '' ? undefined : Number(config.flapDwellSeconds),
      });
      onCreated(created);
    } catch (err) {
      setProblem(err instanceof ApiError ? err.problem : { title: 'Create failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [canSubmit, trimmedAssetId, trimmedMetric, config, onCreated]);

  return (
    <Modal
      visible
      header="Create alert rule"
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
              <Alert type="error" header="Could not create the alert rule">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <FormField label="Asset ID" errorText={assetIdError}>
            <Input value={assetId} onChange={({ detail }) => setAssetId(detail.value)} disabled={busy} ariaLabel="Asset ID" placeholder="Required" />
          </FormField>
          <FormField label="Metric" description="Lower-case snake_case, e.g. cpu_utilization" errorText={metricError}>
            <Input value={metric} onChange={({ detail }) => setMetric(detail.value)} disabled={busy} ariaLabel="Metric" placeholder="Required" />
          </FormField>
          <AlertRuleConfigFields
            values={config}
            onChange={setConfig}
            disabled={busy}
            errors={{ threshold: thresholdError, forDurationSeconds: forDurationError, flapDwellSeconds: flapDwellError }}
          />
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

/**
 * The alerts board: every alert rule for the caller's tenant (bounded at
 * ALERT_RULE_LIST_CAP), filterable by state and severity, sortable, and
 * drilling into a rule's own detail through the shell's `SplitPanel` — the
 * same pattern the incident board (ADR-NOC-004) established. New in E-ACT.2
 * (ADR-ACT-002): "Create rule" from this board, and edit/enable-disable/
 * delete from the detail panel it opens. "Linked incident" (E-HARD.3, closing
 * the gap ADR-NOC-005 recorded) reuses the exact same drill-down idiom the
 * incident board itself uses to open one (`IncidentBoardPage`'s `openIncident`):
 * `openSplitPanel` with `IncidentDetailPanel`, not a new route — a rule with
 * no `current_incident_id` renders an em dash instead of a link.
 */
export function AlertsBoardPage() {
  const { openSplitPanel, closeSplitPanel } = useOutletContext<ShellSplitPanelContext>();

  const [stateFilter, setStateFilter] = useState<AlertRuleState | ''>('');
  const [severityFilter, setSeverityFilter] = useState<AlertSeverity | ''>('');
  const [rawItems, setRawItems] = useState<AlertRuleDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);
  const [currentPageIndex, setCurrentPageIndex] = useState(1);
  const [createOpen, setCreateOpen] = useState(false);

  /** Reruns the board's own fetch — shared by error-retry, a successful create, and any edit/enable-disable/delete from the detail panel. */
  const reload = useCallback(() => setReloads((n) => n + 1), []);

  const openRule = useCallback(
    (rule: AlertRuleDTO) => {
      openSplitPanel(
        `${rule.asset_id} · ${rule.metric}`,
        <AlertRuleDetailPanel
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

  /** Same idiom `IncidentBoardPage`'s own `openIncident` uses — a `SplitPanel` over `IncidentDetailPanel`, not a new route. */
  const openIncident = useCallback(
    (incidentId: string) => {
      openSplitPanel(`Incident ${incidentId}`, <IncidentDetailPanel incidentId={incidentId} />);
    },
    [openSplitPanel],
  );

  const columns = useMemo<TableProps.ColumnDefinition<AlertRuleDTO>[]>(
    () => [
      {
        id: 'rule',
        header: 'Rule',
        isRowHeader: true,
        sortingComparator: (a, b) => `${a.asset_id}${a.metric}`.localeCompare(`${b.asset_id}${b.metric}`),
        cell: (rule) => (
          <div>
            <Button variant="inline-link" onClick={() => openRule(rule)}>
              {rule.asset_id} · {rule.metric}
            </Button>
            <Box fontSize="body-s" color="text-body-secondary">
              {rule.rule_id}
            </Box>
          </div>
        ),
      },
      {
        id: 'severity',
        header: 'Severity',
        sortingComparator: bySeverityThenState,
        cell: (rule) => <StatusIndicator type={ALERT_SEVERITY_TYPE[rule.severity]}>{humanise(rule.severity)}</StatusIndicator>,
      },
      {
        id: 'state',
        header: 'State',
        sortingComparator: (a, b) => ALERT_STATE_RANK[a.last_state] - ALERT_STATE_RANK[b.last_state],
        cell: (rule) => <StatusIndicator type={ALERT_STATE_TYPE[rule.last_state]}>{humanise(rule.last_state)}</StatusIndicator>,
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
      {
        id: 'symptom_class',
        header: 'Symptom class',
        sortingComparator: (a, b) => a.symptom_class.localeCompare(b.symptom_class),
        cell: (rule) => humanise(rule.symptom_class),
      },
      {
        id: 'threshold',
        header: 'Threshold',
        sortingComparator: (a, b) => a.threshold - b.threshold,
        cell: (rule) => `${COMPARATOR_SYMBOL[rule.comparator]} ${rule.threshold}`,
      },
      {
        id: 'enabled',
        header: 'Enabled',
        sortingComparator: (a, b) => Number(b.enabled) - Number(a.enabled),
        cell: (rule) => (
          <StatusIndicator type={rule.enabled ? 'success' : 'stopped'}>
            {rule.enabled ? 'Enabled' : 'Disabled'}
          </StatusIndicator>
        ),
      },
    ],
    [openRule, openIncident],
  );

  const [sortState, setSortState] = useState<{ column: TableProps.ColumnDefinition<AlertRuleDTO>; descending: boolean }>(
    () => ({ column: columns[1], descending: false }), // severity — the default read order.
  );

  const load = useCallback((signal: AbortSignal) => {
    setLoading(true);
    setProblem(null);
    listAlertRules({}, signal)
      .then((page) => {
        setRawItems(page.items ?? []);
        setCurrentPageIndex(1);
      })
      .catch((err: unknown) => {
        if (signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load alert rules', status: 0, detail: String(err) },
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
        (r) => (!stateFilter || r.last_state === stateFilter) && (!severityFilter || r.severity === severityFilter),
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

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Alert rules for this tenant. Click a rule for detail."
        counter={`(${rawItems.length})`}
        actions={<Button onClick={() => setCreateOpen(true)}>Create rule</Button>}
      >
        Alerts
      </Header>

      <SpaceBetween direction="horizontal" size="s" alignItems="center">
        <Select
          selectedOption={selectedState}
          onChange={({ detail }) => setStateFilter((detail.selectedOption.value ?? '') as AlertRuleState | '')}
          options={STATE_OPTIONS}
          ariaLabel="Filter by state"
        />
        <Select
          selectedOption={selectedSeverity}
          onChange={({ detail }) => setSeverityFilter((detail.selectedOption.value ?? '') as AlertSeverity | '')}
          options={SEVERITY_OPTIONS}
          ariaLabel="Filter by severity"
        />
        <Box color="text-body-secondary" fontSize="body-s">
          {filtered.length} of {rawItems.length} rule{rawItems.length === 1 ? '' : 's'}
        </Box>
      </SpaceBetween>

      {rawItems.length >= ALERT_RULE_LIST_CAP && (
        <Box color="text-status-warning" fontSize="body-s">
          Showing the first {ALERT_RULE_LIST_CAP} alert rules for this tenant — narrow further with a future
          server-side filter if this tenant has more.
        </Box>
      )}

      {problem && <ErrorState problem={problem} onRetry={reload} />}

      {!problem && (
        <Table
          items={pageItems}
          columnDefinitions={columns}
          trackBy="rule_id"
          loading={loading}
          loadingText="Loading alert rules"
          variant="container"
          sortingColumn={sortState.column}
          sortingDescending={sortState.descending}
          onSortingChange={({ detail }) =>
            setSortState({
              column: detail.sortingColumn as TableProps.ColumnDefinition<AlertRuleDTO>,
              descending: Boolean(detail.isDescending),
            })
          }
          ariaLabels={{ tableLabel: 'Alert rules' }}
          empty={<AlertRulesEmpty hasFilter={hasFilter} onClear={clearFilters} />}
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
        <CreateAlertRuleModal
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
