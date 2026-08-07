import { useCallback, useEffect, useState } from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { SEVERITY_TYPE, humanise } from '../incidentPresentation';
import {
  actionConfigFromJsonText,
  actionConfigFromWebhookUrl,
  actionConfigToJsonText,
  deleteSecurityResponseRule,
  getSecurityResponseRule,
  patchSecurityResponseRule,
  securityResponseRuleActionConfigJsonError,
  securityResponseRuleNameError,
  securityResponseRuleWebhookUrlError,
  webhookUrlFromActionConfig,
} from '../securityResponseRules';
import type { SecurityResponseRuleDTO } from '../securityResponseRules';
import { ActionConfigEditor, SecurityResponseRuleConfigFields } from './SecurityResponseRuleForm';
import type { SecurityResponseRuleConfigValues } from './SecurityResponseRuleForm';
import { ErrorState } from './States';

/**
 * "Edit rule" (E-SEC-UI.4): name/min_severity/action_config/enabled — the
 * same four fields `patchSecurityResponseRuleRequest` accepts, pre-filled
 * from the rule just read, PATCHed with the `row_version` that same read
 * carried. asset_id/action_type are shown read-only above the form —
 * `SecurityResponseRulePatchInput` cannot change them (see
 * `securityResponseRules.ts`'s own doc comment: delete and recreate to
 * repoint either).
 */
function EditSecurityResponseRuleModal({
  rule,
  onClose,
  onSaved,
  onConflict,
}: {
  rule: SecurityResponseRuleDTO;
  onClose: () => void;
  onSaved: () => void;
  onConflict: () => void;
}) {
  const [config, setConfig] = useState<SecurityResponseRuleConfigValues>({
    name: rule.name,
    minSeverity: rule.min_severity,
    enabled: rule.enabled,
  });
  const [actionConfigValue, setActionConfigValue] = useState(
    rule.action_type === 'http' ? webhookUrlFromActionConfig(rule.action_config) : actionConfigToJsonText(rule.action_config),
  );
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const nameError = securityResponseRuleNameError(config.name);
  const actionConfigError =
    rule.action_type === 'http'
      ? securityResponseRuleWebhookUrlError(actionConfigValue)
      : securityResponseRuleActionConfigJsonError(actionConfigValue);

  const canSubmit = config.name.trim().length > 0 && !nameError && actionConfigValue.trim().length > 0 && !actionConfigError && !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      const actionConfig =
        rule.action_type === 'http' ? actionConfigFromWebhookUrl(actionConfigValue) : actionConfigFromJsonText(actionConfigValue);
      await patchSecurityResponseRule(
        rule.rule_id,
        {
          name: config.name.trim(),
          min_severity: config.minSeverity,
          action_config: actionConfig,
          enabled: config.enabled,
        },
        rule.row_version,
      );
      onSaved();
    } catch (err) {
      if (err instanceof ApiError && err.problem.status === 409) {
        onConflict();
      } else {
        setProblem(err instanceof ApiError ? err.problem : { title: 'Update failed', status: 0, detail: String(err) });
      }
    } finally {
      setBusy(false);
    }
  }, [canSubmit, rule, config, actionConfigValue, onSaved, onConflict]);

  return (
    <Modal
      visible
      header={`Edit ${rule.name}`}
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
              Save
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Form>
        <SpaceBetween size="m">
          {problem && (
            <div role="alert">
              <Alert type="error" header="Could not update the response rule">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <Box color="text-body-secondary" fontSize="body-s">
            Asset {rule.asset_id ?? 'any'} · action {humanise(rule.action_type)} — fixed at creation, not editable here.
          </Box>
          <SecurityResponseRuleConfigFields values={config} onChange={setConfig} disabled={busy} errors={{ name: nameError }} />
          <ActionConfigEditor
            actionType={rule.action_type}
            value={actionConfigValue}
            onChange={setActionConfigValue}
            disabled={busy}
            error={actionConfigError}
          />
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

/**
 * The `SplitPanel` content for one response rule, reusing `GET
 * /v1/admin/security-response-rules/{id}` unchanged — the same load/error
 * structure `SecurityRuleDetailPanel` establishes. Edit, Enable/Disable and
 * Delete all follow the same optimistic-lock/409/refetch discipline
 * ADR-ACT-001/ADR-ACT-002 set — except Delete, which the confirmed contract
 * carries no `row_version` for at all (see `deleteSecurityResponseRule`'s
 * doc comment). `onChanged` refreshes the host board after edit/
 * enable-disable; `onDeleted` additionally tells the host to close this
 * panel, since its subject no longer exists.
 */
export function SecurityResponseRuleDetailPanel({
  ruleId,
  onChanged,
  onDeleted,
}: {
  ruleId: string;
  onChanged?: () => void;
  onDeleted?: () => void;
}) {
  const [rule, setRule] = useState<SecurityResponseRuleDTO | null>(null);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [retries, setRetries] = useState(0);

  const [busy, setBusy] = useState(false);
  const [actionProblem, setActionProblem] = useState<ProblemDetail | null>(null);
  const [conflictNotice, setConflictNotice] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const refetch = useCallback(() => setRetries((n) => n + 1), []);

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);

    getSecurityResponseRule(ruleId, ctrl.signal)
      .then(setRule)
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(
          err instanceof ApiError ? err.problem : { title: 'Could not load response rule', status: 0, detail: String(err) },
        );
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });

    return () => ctrl.abort();
  }, [ruleId, retries]);

  /** Runs after an edit/enable-disable mutation: success and 409 both refetch; only other errors leave state as-is. */
  const afterMutation = useCallback(
    (outcome: 'ok' | 'conflict') => {
      setConflictNotice(outcome === 'conflict');
      refetch();
      onChanged?.();
    },
    [refetch, onChanged],
  );

  const toggleEnabled = useCallback(async () => {
    if (!rule) return;
    setBusy(true);
    setActionProblem(null);
    try {
      await patchSecurityResponseRule(rule.rule_id, { enabled: !rule.enabled }, rule.row_version);
      afterMutation('ok');
    } catch (err) {
      if (err instanceof ApiError && err.problem.status === 409) {
        afterMutation('conflict');
      } else {
        setActionProblem(err instanceof ApiError ? err.problem : { title: 'Update failed', status: 0, detail: String(err) });
      }
    } finally {
      setBusy(false);
    }
  }, [rule, afterMutation]);

  const runDelete = useCallback(async () => {
    if (!rule) return;
    setBusy(true);
    setActionProblem(null);
    try {
      await deleteSecurityResponseRule(rule.rule_id);
      setConfirmDelete(false);
      onDeleted?.();
    } catch (err) {
      setConfirmDelete(false);
      setActionProblem(err instanceof ApiError ? err.problem : { title: 'Delete failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [rule, onDeleted]);

  if (problem) return <ErrorState problem={problem} onRetry={refetch} />;

  if (loading && !rule) {
    return (
      <Box color="text-body-secondary" padding="l">
        Loading response rule…
      </Box>
    );
  }

  if (!rule) return null;

  return (
    <SpaceBetween size="l">
      {conflictNotice && (
        <div role="alert">
          <Alert type="warning" header="Changed since you loaded it" dismissible onDismiss={() => setConflictNotice(false)}>
            This response rule was modified concurrently. It has been reloaded to the current state below — review it
            and try again.
          </Alert>
        </div>
      )}

      {actionProblem && (
        <div role="alert">
          <Alert type="error" header="Action failed" dismissible onDismiss={() => setActionProblem(null)}>
            {actionProblem.detail ?? `The server returned ${actionProblem.status}.`}
          </Alert>
        </div>
      )}

      <Box color="text-body-secondary" fontSize="body-s">
        SAFE action only: this rule can send a webhook or an internal notification when it fires. It cannot act on the
        incident's asset — destructive or autonomous response is not available here (ADR-SOC-010).
      </Box>

      <SpaceBetween direction="horizontal" size="xs">
        <Button disabled={busy} onClick={() => setEditOpen(true)}>
          Edit
        </Button>
        <Button disabled={busy} loading={busy} onClick={() => void toggleEnabled()}>
          {rule.enabled ? 'Disable' : 'Enable'}
        </Button>
        <Button disabled={busy} onClick={() => setConfirmDelete(true)}>
          Delete
        </Button>
      </SpaceBetween>

      <KeyValuePairs
        columns={2}
        items={[
          { label: 'Name', value: rule.name },
          { label: 'Asset', value: rule.asset_id ?? 'Any asset' },
          {
            label: 'Minimum severity',
            value: <StatusIndicator type={SEVERITY_TYPE[rule.min_severity]}>{humanise(rule.min_severity)}</StatusIndicator>,
          },
          { label: 'Action type', value: humanise(rule.action_type) },
          {
            label: 'Enabled',
            value: <StatusIndicator type={rule.enabled ? 'success' : 'stopped'}>{rule.enabled ? 'Enabled' : 'Disabled'}</StatusIndicator>,
          },
          { label: 'Created', value: new Date(rule.created_at).toLocaleString() },
          { label: 'Updated', value: new Date(rule.updated_at).toLocaleString() },
        ]}
      />

      <Box>
        <Box variant="awsui-key-label">Action config</Box>
        <Box variant="code" padding={{ top: 'xs' }}>
          {rule.action_type === 'http' ? webhookUrlFromActionConfig(rule.action_config) : actionConfigToJsonText(rule.action_config)}
        </Box>
      </Box>

      {editOpen && (
        <EditSecurityResponseRuleModal
          rule={rule}
          onClose={() => setEditOpen(false)}
          onSaved={() => {
            setEditOpen(false);
            afterMutation('ok');
          }}
          onConflict={() => {
            setEditOpen(false);
            afterMutation('conflict');
          }}
        />
      )}

      {confirmDelete && (
        <Modal
          visible
          header={`Delete ${rule.name}?`}
          onDismiss={() => {
            if (!busy) setConfirmDelete(false);
          }}
          closeAriaLabel="Dismiss"
          footer={
            <Box float="right">
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="link" onClick={() => setConfirmDelete(false)} disabled={busy}>
                  Cancel
                </Button>
                <Button variant="primary" loading={busy} onClick={() => void runDelete()}>
                  Delete
                </Button>
              </SpaceBetween>
            </Box>
          }
        >
          <Box>
            This permanently removes the rule — it will no longer be evaluated, and this cannot be undone. Unlike
            Edit, there is no optimistic-lock check on delete (the endpoint takes no row_version — see ADR-HARD-003).
          </Box>
        </Modal>
      )}
    </SpaceBetween>
  );
}
