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
import { humanise } from '../incidentPresentation';
import { RISK_STATUS_TYPE } from '../riskPresentation';
import { getRisk, legalRiskTransitions, patchRisk, riskCategoryError, setRiskStatus } from '../risks';
import type { RiskDTO, RiskStatus } from '../risks';
import { RiskFormFields } from './RiskForm';
import type { RiskFormValues } from './RiskForm';
import { ErrorState } from './States';

function toFormValues(risk: RiskDTO): RiskFormValues {
  return {
    title: risk.title,
    description: risk.description,
    category: risk.category,
    likelihood: risk.likelihood,
    impact: risk.impact,
    treatment: risk.treatment ?? '',
    assetId: risk.asset_id ?? '',
  };
}

/**
 * "Edit risk" (E-SEC-UI.3): every non-lifecycle field in one PATCH call,
 * pre-filled from the risk just read, guarded by the `row_version` that same
 * read carried — mirrors `EditSecurityRuleModal`'s exact shape.
 */
function EditRiskModal({
  risk,
  onClose,
  onSaved,
  onConflict,
}: {
  risk: RiskDTO;
  onClose: () => void;
  onSaved: () => void;
  onConflict: () => void;
}) {
  const [values, setValues] = useState<RiskFormValues>(toFormValues(risk));
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const trimmedTitle = values.title.trim();
  const canSubmit = trimmedTitle.length > 0 && !riskCategoryError(values.category) && !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      await patchRisk(
        risk.risk_id,
        {
          title: trimmedTitle,
          description: values.description,
          category: values.category.trim(),
          likelihood: values.likelihood,
          impact: values.impact,
          treatment: values.treatment,
          asset_id: values.assetId.trim(),
        },
        risk.row_version,
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
  }, [canSubmit, risk, trimmedTitle, values, onSaved, onConflict]);

  return (
    <Modal
      visible
      header={`Edit ${risk.title}`}
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
              <Alert type="error" header="Could not update the risk">
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

/** Label for a transition BUTTON, keyed by its TARGET status — mirrors IncidentDetail/VulnFindingDetail's TRANSITION_LABEL. */
const TRANSITION_LABEL: Partial<Record<RiskStatus, string>> = {
  mitigating: 'Start mitigating',
  accepted: 'Accept risk',
  closed: 'Close',
  open: 'Reopen',
};

/**
 * Consequential transitions (ADR-ACT-001 §1 pattern): `accepted` and
 * `closed` are operator JUDGMENTS about the risk's disposition — confirmed
 * via `Modal` before sending, mirroring VulnFindingDetail's identical
 * treatment of `accepted_risk`. `mitigating`/`open` (re-triage) are routine
 * and fire directly.
 */
const CONSEQUENTIAL: Partial<Record<RiskStatus, string>> = {
  accepted: 'This records a deliberate decision to accept this risk as-is. It can still be reopened or mitigated later.',
  closed: 'This closes the risk — it no longer applies. It can still be reopened later if it resurfaces.',
};

/**
 * The `SplitPanel` content for one risk-register entry (E-SEC-UI.3): its
 * detail fields, legal-only status transitions, and full-field Edit —
 * mirrors `VulnFindingDetailPanel`'s exact write-action shape (ADR-ACT-001):
 * disable-while-pending, refetch-after, 409-refetches-with-notice-never-
 * blind-retries, confirm-before-judgment.
 */
export function RiskDetailPanel({ riskId, onChanged }: { riskId: string; onChanged?: () => void }) {
  const [risk, setRisk] = useState<RiskDTO | null>(null);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [retries, setRetries] = useState(0);

  const [busy, setBusy] = useState(false);
  const [actionProblem, setActionProblem] = useState<ProblemDetail | null>(null);
  const [conflictNotice, setConflictNotice] = useState(false);
  const [confirmTarget, setConfirmTarget] = useState<RiskStatus | null>(null);
  const [editOpen, setEditOpen] = useState(false);

  const refetch = useCallback(() => setRetries((n) => n + 1), []);

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);

    getRisk(riskId, ctrl.signal)
      .then(setRisk)
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(err instanceof ApiError ? err.problem : { title: 'Could not load risk', status: 0, detail: String(err) });
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });

    return () => ctrl.abort();
  }, [riskId, retries]);

  /** Runs after ANY mutation attempt: success and 409 both refetch; only other errors leave the state as-is for the operator to read/retry. */
  const afterMutation = useCallback(
    (outcome: 'ok' | 'conflict') => {
      setConflictNotice(outcome === 'conflict');
      refetch();
      onChanged?.();
    },
    [refetch, onChanged],
  );

  const runTransition = useCallback(
    async (target: RiskStatus) => {
      if (!risk) return;
      setBusy(true);
      setActionProblem(null);
      try {
        await setRiskStatus(risk.risk_id, risk.row_version, target);
        setConfirmTarget(null);
        afterMutation('ok');
      } catch (err) {
        setConfirmTarget(null);
        if (err instanceof ApiError && err.problem.status === 409) {
          afterMutation('conflict');
        } else {
          setActionProblem(err instanceof ApiError ? err.problem : { title: 'Transition failed', status: 0, detail: String(err) });
        }
      } finally {
        setBusy(false);
      }
    },
    [risk, afterMutation],
  );

  const onTransitionClick = (target: RiskStatus) => {
    setConflictNotice(false);
    if (CONSEQUENTIAL[target]) {
      setConfirmTarget(target);
      return;
    }
    void runTransition(target);
  };

  if (problem) return <ErrorState problem={problem} onRetry={refetch} />;

  if (loading && !risk) {
    return (
      <Box color="text-body-secondary" padding="l">
        Loading risk…
      </Box>
    );
  }

  if (!risk) return null;

  const transitions = legalRiskTransitions(risk.status);

  return (
    <SpaceBetween size="l">
      {conflictNotice && (
        <div role="alert">
          <Alert type="warning" header="Changed since you loaded it" dismissible onDismiss={() => setConflictNotice(false)}>
            This risk was modified concurrently. It has been reloaded to the current state below — review it and try
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
        {transitions.map((target) => (
          <Button key={target} disabled={busy} loading={busy && confirmTarget === null} onClick={() => onTransitionClick(target)}>
            {TRANSITION_LABEL[target] ?? humanise(target)}
          </Button>
        ))}
      </SpaceBetween>

      <KeyValuePairs
        columns={2}
        items={[
          { label: 'Title', value: risk.title },
          { label: 'Category', value: risk.category || <Box color="text-body-secondary">Uncategorised</Box> },
          { label: 'Likelihood', value: humanise(risk.likelihood) },
          { label: 'Impact', value: humanise(risk.impact) },
          {
            label: 'Status',
            value: <StatusIndicator type={RISK_STATUS_TYPE[risk.status]}>{humanise(risk.status)}</StatusIndicator>,
          },
          { label: 'Treatment', value: risk.treatment ? humanise(risk.treatment) : <Box color="text-body-secondary">Not yet decided</Box> },
          { label: 'Asset', value: risk.asset_id ?? <Box color="text-body-secondary">Unlinked</Box> },
          { label: 'Created', value: new Date(risk.created_at).toLocaleString() },
          { label: 'Updated', value: new Date(risk.updated_at).toLocaleString() },
        ]}
      />

      {risk.description && (
        <Box>
          <Box variant="awsui-key-label">Description</Box>
          <Box>{risk.description}</Box>
        </Box>
      )}

      {editOpen && (
        <EditRiskModal
          risk={risk}
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

      {confirmTarget && (
        <Modal
          visible
          header={`${TRANSITION_LABEL[confirmTarget] ?? humanise(confirmTarget)} ${risk.risk_id}?`}
          onDismiss={() => {
            if (!busy) setConfirmTarget(null);
          }}
          closeAriaLabel="Dismiss"
          footer={
            <Box float="right">
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="link" onClick={() => setConfirmTarget(null)} disabled={busy}>
                  Cancel
                </Button>
                <Button variant="primary" loading={busy} onClick={() => void runTransition(confirmTarget)}>
                  {TRANSITION_LABEL[confirmTarget] ?? humanise(confirmTarget)}
                </Button>
              </SpaceBetween>
            </Box>
          }
        >
          <Box>{CONSEQUENTIAL[confirmTarget]}</Box>
        </Modal>
      )}
    </SpaceBetween>
  );
}
