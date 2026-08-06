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
import { ALERT_SEVERITY_TYPE, ALERT_STATE_TYPE, COMPARATOR_SYMBOL } from '../alertPresentation';
import {
  alertRuleFlapDwellError,
  alertRuleForDurationError,
  alertRuleThresholdError,
  deleteAlertRule,
  getAlertRule,
  patchAlertRule,
} from '../alertRules';
import type { AlertRuleDTO } from '../alertRules';
import { AlertRuleConfigFields } from './AlertRuleForm';
import type { AlertRuleConfigValues } from './AlertRuleForm';
import { humanise } from '../incidentPresentation';
import { ErrorState } from './States';

/**
 * "Edit rule" (ADR-ACT-002): the same seven-field form the create modal
 * uses, pre-filled from the rule just read, PATCHed with the `row_version`
 * that same read carried. asset_id/metric are shown read-only above the
 * form — `patchAlertRuleRequest` cannot change them (see
 * `alertRules.ts`'s `AlertRulePatchInput` doc comment).
 */
function EditAlertRuleModal({
  rule,
  onClose,
  onSaved,
  onConflict,
}: {
  rule: AlertRuleDTO;
  onClose: () => void;
  onSaved: () => void;
  onConflict: () => void;
}) {
  const [config, setConfig] = useState<AlertRuleConfigValues>({
    comparator: rule.comparator,
    threshold: String(rule.threshold),
    forDurationSeconds: String(rule.for_duration_seconds),
    severity: rule.severity,
    symptomClass: rule.symptom_class,
    flapDwellSeconds: String(rule.flap_dwell_seconds),
    enabled: rule.enabled,
  });
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const thresholdError = alertRuleThresholdError(config.threshold);
  const forDurationError = alertRuleForDurationError(config.forDurationSeconds);
  const flapDwellError = alertRuleFlapDwellError(config.flapDwellSeconds);
  const canSubmit =
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
      await patchAlertRule(
        rule.rule_id,
        {
          comparator: config.comparator,
          threshold: Number(config.threshold),
          for_duration_seconds: Number(config.forDurationSeconds),
          severity: config.severity,
          symptom_class: config.symptomClass,
          enabled: config.enabled,
          flap_dwell_seconds: config.flapDwellSeconds.trim() === '' ? undefined : Number(config.flapDwellSeconds),
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
  }, [canSubmit, rule, config, onSaved, onConflict]);

  return (
    <Modal
      visible
      header={`Edit ${rule.asset_id} · ${rule.metric}`}
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
              <Alert type="error" header="Could not update the alert rule">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <Box color="text-body-secondary" fontSize="body-s">
            Asset {rule.asset_id} · metric {rule.metric} — fixed at creation, not editable here.
          </Box>
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
 * The `SplitPanel` content for one alert rule, reusing `GET
 * /v1/admin/alert-rules/{id}` unchanged — the same load/error structure
 * `IncidentDetailPanel` establishes. New in E-ACT.2 (ADR-ACT-002): Edit,
 * Enable/Disable, and Delete, all under the same optimistic-lock/409/refetch
 * discipline ADR-ACT-001 set — except Delete, which the confirmed contract
 * carries no `row_version` for at all (see `deleteAlertRule`'s doc comment).
 * `onChanged` refreshes the host board after edit/enable-disable; `onDeleted`
 * additionally tells the host to close this panel, since its subject no
 * longer exists.
 */
export function AlertRuleDetailPanel({
  ruleId,
  onChanged,
  onDeleted,
}: {
  ruleId: string;
  onChanged?: () => void;
  onDeleted?: () => void;
}) {
  const [rule, setRule] = useState<AlertRuleDTO | null>(null);
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

    getAlertRule(ruleId, ctrl.signal)
      .then(setRule)
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load alert rule', status: 0, detail: String(err) },
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
      await patchAlertRule(rule.rule_id, { enabled: !rule.enabled }, rule.row_version);
      afterMutation('ok');
    } catch (err) {
      if (err instanceof ApiError && err.problem.status === 409) {
        afterMutation('conflict');
      } else {
        setActionProblem(
          err instanceof ApiError ? err.problem : { title: 'Update failed', status: 0, detail: String(err) },
        );
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
      await deleteAlertRule(rule.rule_id);
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
        Loading alert rule…
      </Box>
    );
  }

  if (!rule) return null;

  return (
    <SpaceBetween size="l">
      {conflictNotice && (
        <div role="alert">
          <Alert type="warning" header="Changed since you loaded it" dismissible onDismiss={() => setConflictNotice(false)}>
            This alert rule was modified concurrently. It has been reloaded to the current state below — review it and try
            again.
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
          { label: 'Asset', value: rule.asset_id },
          { label: 'Metric', value: rule.metric },
          {
            label: 'Severity',
            value: <StatusIndicator type={ALERT_SEVERITY_TYPE[rule.severity]}>{humanise(rule.severity)}</StatusIndicator>,
          },
          {
            label: 'State',
            value: <StatusIndicator type={ALERT_STATE_TYPE[rule.last_state]}>{humanise(rule.last_state)}</StatusIndicator>,
          },
          { label: 'Threshold', value: `${COMPARATOR_SYMBOL[rule.comparator]} ${rule.threshold}` },
          { label: 'For duration', value: `${rule.for_duration_seconds}s` },
          { label: 'Symptom class', value: humanise(rule.symptom_class) },
          {
            label: 'Enabled',
            value: <StatusIndicator type={rule.enabled ? 'success' : 'stopped'}>{rule.enabled ? 'Enabled' : 'Disabled'}</StatusIndicator>,
          },
          { label: 'Flap dwell', value: `${rule.flap_dwell_seconds}s` },
          {
            label: 'Last transition',
            value: rule.last_transition_at
              ? new Date(rule.last_transition_at).toLocaleString()
              : <Box color="text-body-secondary">Never evaluated</Box>,
          },
          { label: 'Created', value: new Date(rule.created_at).toLocaleString() },
          { label: 'Updated', value: new Date(rule.updated_at).toLocaleString() },
        ]}
      />
      <Box color="text-body-secondary" fontSize="body-s">
        This rule's linked incident (if any) is not part of the current alert-rule
        contract — see ADR-NOC-005.
      </Box>

      {editOpen && (
        <EditAlertRuleModal
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
          header={`Delete ${rule.asset_id} · ${rule.metric}?`}
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
            This permanently removes the rule — it will no longer be evaluated, and this cannot be undone. Unlike Edit,
            there is no optimistic-lock check on delete (the endpoint takes no row_version — see ADR-ACT-002).
          </Box>
        </Modal>
      )}
    </SpaceBetween>
  );
}
