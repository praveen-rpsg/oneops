import { useCallback, useEffect, useMemo, useState } from 'react';
import { useOutletContext } from 'react-router-dom';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Header from '@cloudscape-design/components/header';
import Pagination from '@cloudscape-design/components/pagination';
import Select from '@cloudscape-design/components/select';
import type { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import type { TableProps } from '@cloudscape-design/components/table';
import type { ShellSplitPanelContext } from '../Shell';
import { ALERT_SEVERITY_RANK, ALERT_SEVERITY_TYPE, ALERT_STATE_RANK, ALERT_STATE_TYPE, COMPARATOR_SYMBOL } from '../alertPresentation';
import { ALERT_RULE_LIST_CAP, ALERT_RULE_STATES, ALERT_SEVERITIES, listAlertRules } from '../alertRules';
import type { AlertRuleDTO, AlertRuleState, AlertSeverity } from '../alertRules';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { AlertRuleDetailPanel } from '../components/AlertRuleDetail';
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
 * The alerts board: every alert rule for the caller's tenant (bounded at
 * ALERT_RULE_LIST_CAP), filterable by state and severity, sortable, and
 * drilling into a rule's own detail through the shell's `SplitPanel` — the
 * same pattern the incident board (ADR-NOC-004) established. No "linked
 * incident" column: current_incident_id is not part of the existing
 * alert-rule HTTP contract (see ADR-NOC-005).
 */
export function AlertsBoardPage() {
  const { openSplitPanel } = useOutletContext<ShellSplitPanelContext>();

  const [stateFilter, setStateFilter] = useState<AlertRuleState | ''>('');
  const [severityFilter, setSeverityFilter] = useState<AlertSeverity | ''>('');
  const [rawItems, setRawItems] = useState<AlertRuleDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);
  const [currentPageIndex, setCurrentPageIndex] = useState(1);

  const openRule = useCallback(
    (rule: AlertRuleDTO) => {
      openSplitPanel(`${rule.asset_id} · ${rule.metric}`, <AlertRuleDetailPanel ruleId={rule.rule_id} />);
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
    [openRule],
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

  const retry = () => setReloads((n) => n + 1);
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

      {problem && <ErrorState problem={problem} onRetry={retry} />}

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
    </SpaceBetween>
  );
}
