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
import { OBSERVATION_SEVERITY_TYPE, SECURITY_RULE_STATE_TYPE } from '../securityRulePresentation';
import { SEVERITY_TYPE } from '../incidentPresentation';
import {
  deleteSecurityRule,
  getSecurityRule,
  securityRuleThresholdCountError,
  securityRuleWindowSecondsError,
  patchSecurityRule,
} from '../securityRules';
import type { SecurityRuleDTO } from '../securityRules';
import { SecurityRuleConfigFields } from './SecurityRuleForm';
import type { SecurityRuleConfigValues } from './SecurityRuleForm';
import { humanise } from '../incidentPresentation';
import { ErrorState } from './States';

/**
 * "Edit rule" (E-SEC-UI.2): the same five-field form the create modal uses,
 * pre-filled from the rule just read, PATCHed with the `row_version` that
 * same read carried. asset_id/observation_type are shown read-only above the
 * form — `patchSecurityRuleRequest` cannot change them (see
 * `securityRules.ts`'s `SecurityRulePatchInput` doc comment).
 */
function EditSecurityRuleModal({
  rule,
  onClose,
  onSaved,
  onConflict,
}: {
  rule: SecurityRuleDTO;
  onClose: () => void;
  onSaved: () => void;
  onConflict: () => void;
}) {
  const [config, setConfig] = useState<SecurityRuleConfigValues>({
    minSeverity: rule.min_severity,
    windowSeconds: String(rule.window_seconds),
    thresholdCount: String(rule.threshold_count),
    incidentSeverity: rule.incident_severity,
    enabled: rule.enabled,
  });
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const windowSecondsError = securityRuleWindowSecondsError(config.windowSeconds);
  const thresholdCountError = securityRuleThresholdCountError(config.thresholdCount);
  const canSubmit =
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
      await patchSecurityRule(
        rule.rule_id,
        {
          min_severity: config.minSeverity,
          window_seconds: Number(config.windowSeconds),
          threshold_count: Number(config.thresholdCount),
          incident_severity: config.incidentSeverity,
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
  }, [canSubmit, rule, config, onSaved, onConflict]);

  return (
    <Modal
      visible
      header={`Edit ${rule.asset_id} · ${rule.observation_type}`}
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
              <Alert type="error" header="Could not update the detection rule">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <Box color="text-body-secondary" fontSize="body-s">
            Asset {rule.asset_id} · observation type {rule.observation_type} — fixed at creation, not editable here.
          </Box>
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
 * The `SplitPanel` content for one detection rule, reusing `GET
 * /v1/admin/security-rules/{id}` unchanged — the same load/error structure
 * `AlertRuleDetailPanel` establishes. Edit, Enable/Disable and Delete all
 * follow the same optimistic-lock/409/refetch discipline ADR-ACT-001/
 * ADR-ACT-002 set — except Delete, which the confirmed contract carries no
 * `row_version` for at all (see `deleteSecurityRule`'s doc comment).
 * `onChanged` refreshes the host board after edit/enable-disable; `onDeleted`
 * additionally tells the host to close this panel, since its subject no
 * longer exists.
 */
export function SecurityRuleDetailPanel({
  ruleId,
  onChanged,
  onDeleted,
}: {
  ruleId: string;
  onChanged?: () => void;
  onDeleted?: () => void;
}) {
  const [rule, setRule] = useState<SecurityRuleDTO | null>(null);
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

    getSecurityRule(ruleId, ctrl.signal)
      .then(setRule)
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load detection rule', status: 0, detail: String(err) },
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
      await patchSecurityRule(rule.rule_id, { enabled: !rule.enabled }, rule.row_version);
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
      await deleteSecurityRule(rule.rule_id);
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
        Loading detection rule…
      </Box>
    );
  }

  if (!rule) return null;

  return (
    <SpaceBetween size="l">
      {conflictNotice && (
        <div role="alert">
          <Alert type="warning" header="Changed since you loaded it" dismissible onDismiss={() => setConflictNotice(false)}>
            This detection rule was modified concurrently. It has been reloaded to the current state below — review it
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
          { label: 'Observation type', value: rule.observation_type },
          {
            label: 'Minimum severity',
            value: (
              <StatusIndicator type={OBSERVATION_SEVERITY_TYPE[rule.min_severity]}>
                {humanise(rule.min_severity)}
              </StatusIndicator>
            ),
          },
          {
            label: 'State',
            value: (
              <StatusIndicator type={SECURITY_RULE_STATE_TYPE[rule.last_state]}>
                {humanise(rule.last_state)}
              </StatusIndicator>
            ),
          },
          { label: 'Threshold', value: `${rule.threshold_count} in ${rule.window_seconds}s` },
          {
            label: 'Incident severity',
            value: (
              <StatusIndicator type={SEVERITY_TYPE[rule.incident_severity]}>
                {humanise(rule.incident_severity)}
              </StatusIndicator>
            ),
          },
          {
            label: 'Enabled',
            value: <StatusIndicator type={rule.enabled ? 'success' : 'stopped'}>{rule.enabled ? 'Enabled' : 'Disabled'}</StatusIndicator>,
          },
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
        A rule's linked incident (current_incident_id, E8.1b-2) is shown as the board's "Linked incident" column; a
        dedicated row here is a separable follow-up, mirroring AlertRuleDetailPanel's own note.
      </Box>

      {editOpen && (
        <EditSecurityRuleModal
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
          header={`Delete ${rule.asset_id} · ${rule.observation_type}?`}
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
