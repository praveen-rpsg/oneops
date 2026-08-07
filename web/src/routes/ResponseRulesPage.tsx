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
import { SecurityResponseRuleDetailPanel } from '../components/SecurityResponseRuleDetail';
import { ActionConfigEditor, SecurityResponseRuleConfigFields } from '../components/SecurityResponseRuleForm';
import type { SecurityResponseRuleConfigValues } from '../components/SecurityResponseRuleForm';
import { ErrorState } from '../components/States';
import { SEVERITY_RANK, SEVERITY_TYPE, humanise } from '../incidentPresentation';
import {
  INCIDENT_SEVERITIES,
  SAFE_ACTION_TYPES,
  SECURITY_RESPONSE_RULE_LIST_CAP,
  actionConfigFromJsonText,
  actionConfigFromWebhookUrl,
  createSecurityResponseRule,
  listSecurityResponseRules,
  securityResponseRuleActionConfigJsonError,
  securityResponseRuleAssetIdError,
  securityResponseRuleNameError,
  securityResponseRuleWebhookUrlError,
} from '../securityResponseRules';
import type { IncidentSeverity, SecurityResponseActionType, SecurityResponseRuleDTO } from '../securityResponseRules';

/** Client-side page size over the already-fetched set (see securityResponseRules.ts' SECURITY_RESPONSE_RULE_LIST_CAP). */
const PAGE_SIZE = 20;

const ACTION_TYPE_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All action types' },
  ...SAFE_ACTION_TYPES.map((t) => ({ value: t, label: humanise(t) })),
];

const SEVERITY_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'All severities' },
  ...INCIDENT_SEVERITIES.map((s) => ({ value: s, label: humanise(s) })),
];

/** The Select an operator picks a new rule's action_type from — SAFE_ACTION_TYPES ONLY, never any other value policy.DefaultRegistry happens to register. */
const CREATE_ACTION_TYPE_OPTIONS: SelectProps.Option[] = SAFE_ACTION_TYPES.map((t) => ({ value: t, label: humanise(t) }));

function bySeverity(a: SecurityResponseRuleDTO, b: SecurityResponseRuleDTO): number {
  return SEVERITY_RANK[a.min_severity] - SEVERITY_RANK[b.min_severity];
}

function ResponseRulesEmpty({ hasFilter, onClear }: { hasFilter: boolean; onClear: () => void }) {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No response rules{hasFilter ? ' match these filters' : ''}</b>
        <Box variant="p" color="text-body-secondary">
          {hasFilter
            ? 'Try widening the action type or severity filter.'
            : 'No response-automation rules have been registered for this tenant yet.'}
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
        <b>You need tenant-admin permission to manage response rules.</b>
        <Box variant="p" color="text-body-secondary">
          Your account does not currently hold the permission OneOps&apos; security-response-rule administration
          endpoints require. Ask your tenant administrator to grant it.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

/**
 * "Create rule" (E-SEC-UI.4): asset_id (optional) and action_type (fixed at
 * creation — reusing `SecurityResponseRuleConfigFields` for the remaining
 * three fields the create and edit forms share, and `ActionConfigEditor` for
 * the action-type-specific config). Client-validated before submit,
 * mirroring `domain.SecurityResponseRule.Validate`'s own bounds; action_type
 * is a `Select` constrained to `SAFE_ACTION_TYPES` — a bad/unsafe enum is
 * impossible to submit through this form.
 */
function CreateResponseRuleModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (rule: SecurityResponseRuleDTO) => void;
}) {
  const [assetId, setAssetId] = useState('');
  const [actionType, setActionType] = useState<SecurityResponseActionType>('http');
  const [actionConfigValue, setActionConfigValue] = useState('');
  const [config, setConfig] = useState<SecurityResponseRuleConfigValues>({ name: '', minSeverity: 'high', enabled: true });
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const assetIdError = securityResponseRuleAssetIdError(assetId);
  const nameError = securityResponseRuleNameError(config.name);
  const actionConfigError =
    actionType === 'http' ? securityResponseRuleWebhookUrlError(actionConfigValue) : securityResponseRuleActionConfigJsonError(actionConfigValue);

  const trimmedName = config.name.trim();
  const trimmedAssetId = assetId.trim();
  const canSubmit =
    trimmedName.length > 0 &&
    !nameError &&
    !assetIdError &&
    actionConfigValue.trim().length > 0 &&
    !actionConfigError &&
    !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      const actionConfig = actionType === 'http' ? actionConfigFromWebhookUrl(actionConfigValue) : actionConfigFromJsonText(actionConfigValue);
      const created = await createSecurityResponseRule({
        name: trimmedName,
        min_severity: config.minSeverity,
        asset_id: trimmedAssetId || undefined,
        action_type: actionType,
        action_config: actionConfig,
        enabled: config.enabled,
      });
      onCreated(created);
    } catch (err) {
      setProblem(err instanceof ApiError ? err.problem : { title: 'Create failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [canSubmit, trimmedName, trimmedAssetId, actionType, actionConfigValue, config, onCreated]);

  return (
    <Modal
      visible
      header="Create response rule"
      onDismiss={() => {
        if (!busy) onClose();
      }}
      closeAriaLabel="Dismiss"
      size="medium"
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
              <Alert type="error" header="Could not create the response rule">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <Alert type="info" header="SAFE actions only">
            A response rule can only send an outbound webhook or an internal notification when a matching security
            incident occurs — never act on the incident&apos;s own asset. Destructive or autonomous response is not
            available here; it is deferred behind the machine-action attestation model (ADR-SOC-010).
          </Alert>
          <SecurityResponseRuleConfigFields values={config} onChange={setConfig} disabled={busy} errors={{ name: nameError }} />
          <FormField label="Asset ID (optional)" description="Leave blank to match every security incident this tenant raises, regardless of asset." errorText={assetIdError}>
            <Input value={assetId} onChange={({ detail }) => setAssetId(detail.value)} disabled={busy} ariaLabel="Asset ID" placeholder="Optional" />
          </FormField>
          <FormField label="Action type" description="SAFE allowlist only — command and destructive actions are refused server-side.">
            <Select
              selectedOption={CREATE_ACTION_TYPE_OPTIONS.find((o) => o.value === actionType) ?? CREATE_ACTION_TYPE_OPTIONS[0]}
              onChange={({ detail }) => {
                setActionType((detail.selectedOption.value ?? 'http') as SecurityResponseActionType);
                setActionConfigValue('');
              }}
              options={CREATE_ACTION_TYPE_OPTIONS}
              disabled={busy}
              ariaLabel="Action type"
            />
          </FormField>
          <ActionConfigEditor actionType={actionType} value={actionConfigValue} onChange={setActionConfigValue} disabled={busy} error={actionConfigError} />
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

/**
 * The response-rules board (E-SEC-UI.4, ADR-SOC-010): every
 * security_response_rule for the caller's tenant (bounded at
 * SECURITY_RESPONSE_RULE_LIST_CAP), filterable by action type and minimum
 * severity, sortable, and drilling into a rule's own detail through the
 * shell's `SplitPanel` — the same pattern `DetectionRulesPage`/
 * `IndicatorsPage` establish. "Create rule" from this board,
 * edit/enable-disable/delete from the detail panel it opens.
 */
export function ResponseRulesPage() {
  const { openSplitPanel, closeSplitPanel } = useOutletContext<ShellSplitPanelContext>();

  const [actionTypeFilter, setActionTypeFilter] = useState<SecurityResponseActionType | ''>('');
  const [severityFilter, setSeverityFilter] = useState<IncidentSeverity | ''>('');
  const [rawItems, setRawItems] = useState<SecurityResponseRuleDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);
  const [currentPageIndex, setCurrentPageIndex] = useState(1);
  const [createOpen, setCreateOpen] = useState(false);

  const reload = useCallback(() => setReloads((n) => n + 1), []);

  const openRule = useCallback(
    (rule: SecurityResponseRuleDTO) => {
      openSplitPanel(
        rule.name,
        <SecurityResponseRuleDetailPanel
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

  const columns = useMemo<TableProps.ColumnDefinition<SecurityResponseRuleDTO>[]>(
    () => [
      {
        id: 'name',
        header: 'Name',
        isRowHeader: true,
        sortingComparator: (a, b) => a.name.localeCompare(b.name),
        cell: (rule) => (
          <div>
            <Button variant="inline-link" onClick={() => openRule(rule)}>
              {rule.name}
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
        sortingComparator: bySeverity,
        cell: (rule) => <StatusIndicator type={SEVERITY_TYPE[rule.min_severity]}>{humanise(rule.min_severity)}</StatusIndicator>,
      },
      {
        id: 'asset_id',
        header: 'Asset',
        sortingComparator: (a, b) => (a.asset_id ?? '').localeCompare(b.asset_id ?? ''),
        cell: (rule) => rule.asset_id ?? <Box color="text-body-secondary">Any asset</Box>,
      },
      {
        id: 'action_type',
        header: 'Action type',
        sortingComparator: (a, b) => a.action_type.localeCompare(b.action_type),
        cell: (rule) => humanise(rule.action_type),
      },
      {
        id: 'enabled',
        header: 'Enabled',
        sortingComparator: (a, b) => Number(b.enabled) - Number(a.enabled),
        cell: (rule) => (
          <StatusIndicator type={rule.enabled ? 'success' : 'stopped'}>{rule.enabled ? 'Enabled' : 'Disabled'}</StatusIndicator>
        ),
      },
    ],
    [openRule],
  );

  const [sortState, setSortState] = useState<{ column: TableProps.ColumnDefinition<SecurityResponseRuleDTO>; descending: boolean }>(
    () => ({ column: columns[1], descending: false }), // min_severity — the default read order.
  );

  const load = useCallback((signal: AbortSignal) => {
    setLoading(true);
    setProblem(null);
    listSecurityResponseRules({}, signal)
      .then((page) => {
        setRawItems(page.items ?? []);
        setCurrentPageIndex(1);
      })
      .catch((err: unknown) => {
        if (signal.aborted) return;
        setProblem(
          err instanceof ApiError ? err.problem : { title: 'Could not load response rules', status: 0, detail: String(err) },
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
        (r) => (!actionTypeFilter || r.action_type === actionTypeFilter) && (!severityFilter || r.min_severity === severityFilter),
      ),
    [rawItems, actionTypeFilter, severityFilter],
  );

  const sorted = useMemo(() => {
    const cmp = sortState.column.sortingComparator ?? (() => 0);
    const dir = sortState.descending ? -1 : 1;
    return [...filtered].sort((a, b) => cmp(a, b) * dir);
  }, [filtered, sortState]);

  const pagesCount = Math.max(1, Math.ceil(sorted.length / PAGE_SIZE));
  const pageItems = sorted.slice((currentPageIndex - 1) * PAGE_SIZE, currentPageIndex * PAGE_SIZE);

  const clearFilters = () => {
    setActionTypeFilter('');
    setSeverityFilter('');
  };
  const hasFilter = Boolean(actionTypeFilter || severityFilter);
  const selectedActionType = ACTION_TYPE_OPTIONS.find((o) => o.value === actionTypeFilter) ?? ACTION_TYPE_OPTIONS[0];
  const selectedSeverity = SEVERITY_OPTIONS.find((o) => o.value === severityFilter) ?? SEVERITY_OPTIONS[0];
  const forbidden = problem !== null && problem.status === 403;

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="SAFE-only response automation (SOAR) for this tenant's security incidents. Click a rule for detail."
        counter={!forbidden ? `(${rawItems.length})` : undefined}
        actions={<Button onClick={() => setCreateOpen(true)}>Create rule</Button>}
      >
        Response rules
      </Header>

      {!forbidden && (
        <Alert type="info" header="SAFE boundary">
          These rules only run outbound-safe actions (webhook or notification) on matching security incidents.
          Destructive or autonomous response is not available in this console — it is deferred behind the
          machine-action attestation model (ADR-SOC-010).
        </Alert>
      )}

      {!forbidden && (
        <SpaceBetween direction="horizontal" size="s" alignItems="center">
          <Select
            selectedOption={selectedActionType}
            onChange={({ detail }) => setActionTypeFilter((detail.selectedOption.value ?? '') as SecurityResponseActionType | '')}
            options={ACTION_TYPE_OPTIONS}
            ariaLabel="Filter by action type"
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

      {!forbidden && rawItems.length >= SECURITY_RESPONSE_RULE_LIST_CAP && (
        <Box color="text-status-warning" fontSize="body-s">
          Showing the first {SECURITY_RESPONSE_RULE_LIST_CAP} response rules for this tenant — narrow further with a
          future server-side filter if this tenant has more.
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
          loadingText="Loading response rules"
          variant="container"
          sortingColumn={sortState.column}
          sortingDescending={sortState.descending}
          onSortingChange={({ detail }) =>
            setSortState({
              column: detail.sortingColumn as TableProps.ColumnDefinition<SecurityResponseRuleDTO>,
              descending: Boolean(detail.isDescending),
            })
          }
          ariaLabels={{ tableLabel: 'Response rules' }}
          empty={<ResponseRulesEmpty hasFilter={hasFilter} onClear={clearFilters} />}
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
        <CreateResponseRuleModal
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
