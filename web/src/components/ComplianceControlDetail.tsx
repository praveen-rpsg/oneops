import { useCallback, useEffect, useState } from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Modal from '@cloudscape-design/components/modal';
import Select from '@cloudscape-design/components/select';
import type { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Textarea from '@cloudscape-design/components/textarea';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { COMPLIANCE_CONTROL_STATUS_TYPE } from '../compliancePresentation';
import { humanise } from '../incidentPresentation';
import {
  addControlEvidence,
  CONTROL_EVIDENCE_KINDS,
  CONTROL_EVIDENCE_VALUE_MAX_LENGTH,
  getComplianceControl,
  legalComplianceControlTransitions,
  patchComplianceControl,
  setComplianceControlStatus,
} from '../complianceControls';
import type { ComplianceControlStatus, ComplianceControlWithEvidenceDTO, ControlEvidenceDTO, ControlEvidenceKind } from '../complianceControls';
import { ErrorState } from './States';

/** "Edit control" (E-SEC-UI.3): title/description only — framework/control_ref are fixed at creation and shown read-only above the form, mirroring `EditSecurityRuleModal`'s identical treatment of asset_id/observation_type. */
function EditComplianceControlModal({
  control,
  onClose,
  onSaved,
  onConflict,
}: {
  control: ComplianceControlWithEvidenceDTO;
  onClose: () => void;
  onSaved: () => void;
  onConflict: () => void;
}) {
  const [title, setTitle] = useState(control.title);
  const [description, setDescription] = useState(control.description);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const trimmedTitle = title.trim();
  const canSubmit = trimmedTitle.length > 0 && !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      await patchComplianceControl(control.control_id, { title: trimmedTitle, description }, control.row_version);
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
  }, [canSubmit, control, trimmedTitle, description, onSaved, onConflict]);

  return (
    <Modal
      visible
      header={`Edit ${control.framework} ${control.control_ref}`}
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
              <Alert type="error" header="Could not update the compliance control">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <Box color="text-body-secondary" fontSize="body-s">
            {control.framework} {control.control_ref} — fixed at creation, not editable here.
          </Box>
          <FormField label="Title" errorText={title.length > 0 && trimmedTitle.length === 0 ? 'Title is required.' : undefined}>
            <Input value={title} onChange={({ detail }) => setTitle(detail.value)} disabled={busy} ariaLabel="Title" placeholder="Required" />
          </FormField>
          <FormField label="Description" description="Optional — scope, context, interpretation notes">
            <Textarea value={description} onChange={({ detail }) => setDescription(detail.value)} disabled={busy} ariaLabel="Description" />
          </FormField>
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

const EVIDENCE_KIND_OPTIONS: SelectProps.Option[] = CONTROL_EVIDENCE_KINDS.map((k) => ({ value: k, label: humanise(k) }));

/**
 * "Add evidence" (E-SEC-UI.3, ADR-SOC-009): APPENDS one record — there is no
 * edit or delete anywhere in this UI, mirroring `ControlEvidence`'s own
 * schema-enforced append-only contract. Takes no `row_version` (the endpoint
 * accepts none — see `addControlEvidence`'s own doc comment); a successful
 * append simply asks the host to refetch the control, which re-embeds the
 * evidence trail.
 */
function AddEvidenceForm({ controlId, onAdded }: { controlId: string; onAdded: () => void }) {
  const [kind, setKind] = useState<ControlEvidenceKind>('note');
  const [value, setValue] = useState('');
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const trimmedValue = value.trim();
  const canSubmit = trimmedValue.length > 0 && trimmedValue.length <= CONTROL_EVIDENCE_VALUE_MAX_LENGTH && !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      await addControlEvidence(controlId, kind, trimmedValue);
      setValue('');
      onAdded();
    } catch (err) {
      setProblem(err instanceof ApiError ? err.problem : { title: 'Could not file evidence', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [canSubmit, controlId, kind, trimmedValue, onAdded]);

  return (
    <SpaceBetween size="s">
      {problem && (
        <div role="alert">
          <Alert type="error" header="Could not file evidence" dismissible onDismiss={() => setProblem(null)}>
            {problem.detail ?? `The server returned ${problem.status}.`}
          </Alert>
        </div>
      )}
      <FormField label="Kind">
        <Select
          selectedOption={EVIDENCE_KIND_OPTIONS.find((o) => o.value === kind) ?? EVIDENCE_KIND_OPTIONS[0]}
          onChange={({ detail }) => setKind((detail.selectedOption.value ?? 'note') as ControlEvidenceKind)}
          options={EVIDENCE_KIND_OPTIONS}
          disabled={busy}
          ariaLabel="Evidence kind"
        />
      </FormField>
      <FormField
        label="Value"
        description={kind === 'url' ? 'A URL pointing at externally-stored proof' : 'Free text'}
        errorText={
          trimmedValue.length > CONTROL_EVIDENCE_VALUE_MAX_LENGTH
            ? `Must be at most ${CONTROL_EVIDENCE_VALUE_MAX_LENGTH} characters.`
            : undefined
        }
      >
        <Textarea value={value} onChange={({ detail }) => setValue(detail.value)} disabled={busy} ariaLabel="Evidence value" placeholder="Required" />
      </FormField>
      <Box float="right">
        <Button disabled={!canSubmit} loading={busy} onClick={() => void submit()}>
          Add evidence
        </Button>
      </Box>
    </SpaceBetween>
  );
}

/** Absolute time is the evidence; relative time is the convenience — mirrors IncidentDetail's `when()`. */
function when(iso: string): { absolute: string; relative: string } {
  const d = new Date(iso);
  const secs = Math.round((Date.now() - d.getTime()) / 1000);
  const rel =
    secs < 60 ? 'just now'
    : secs < 3600 ? `${Math.floor(secs / 60)} min ago`
    : secs < 86400 ? `${Math.floor(secs / 3600)} h ago`
    : `${Math.floor(secs / 86400)} d ago`;
  return { absolute: d.toLocaleString(), relative: rel };
}

function EvidenceEntry({ evidence }: { evidence: ControlEvidenceDTO }) {
  const t = when(evidence.recorded_at);
  return (
    <li style={{ padding: '10px 0', borderBottom: '1px solid var(--awsui-color-border-divider-default, #e2e5ea)' }}>
      <SpaceBetween size="xxs">
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, flexWrap: 'wrap' }}>
          <Box variant="strong">{humanise(evidence.kind)}</Box>
          <Box>{evidence.recorded_by}</Box>
          <time dateTime={evidence.recorded_at} title={t.absolute}>
            <Box color="text-body-secondary" fontSize="body-s" display="inline">
              {t.relative}
            </Box>
          </time>
        </div>
        <Box fontSize="body-s">{evidence.value}</Box>
      </SpaceBetween>
    </li>
  );
}

/** Label for a transition BUTTON, keyed by its TARGET status — mirrors RiskDetail/VulnFindingDetail's TRANSITION_LABEL. */
const TRANSITION_LABEL: Partial<Record<ComplianceControlStatus, string>> = {
  in_progress: 'Start implementation',
  implemented: 'Mark implemented',
  not_applicable: 'Mark not applicable',
  not_implemented: 'Reset to not implemented',
};

/**
 * Consequential transitions (ADR-ACT-001 §1 pattern): `not_applicable` is an
 * operator JUDGMENT that this control does not apply — confirmed via `Modal`
 * before sending, mirroring RiskDetail's identical treatment of `accepted`.
 * Every other legal transition fires directly.
 */
const CONSEQUENTIAL: Partial<Record<ComplianceControlStatus, string>> = {
  not_applicable:
    'This marks the control as not applicable to your environment. Re-activating it later starts the implementation lifecycle over from not implemented.',
};

/**
 * The `SplitPanel` content for one compliance control (E-SEC-UI.3): its
 * detail fields, legal-only status transitions, title/description Edit, and
 * its append-only evidence trail (newest first) with an "Add evidence" form
 * — mirrors `IncidentDetailPanel`'s timeline shape for the trail and
 * `VulnFindingDetailPanel`'s write-action shape for the rest (ADR-ACT-001):
 * disable-while-pending, refetch-after, 409-refetches-with-notice-never-
 * blind-retries, confirm-before-judgment.
 */
export function ComplianceControlDetailPanel({ controlId, onChanged }: { controlId: string; onChanged?: () => void }) {
  const [control, setControl] = useState<ComplianceControlWithEvidenceDTO | null>(null);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [retries, setRetries] = useState(0);

  const [busy, setBusy] = useState(false);
  const [actionProblem, setActionProblem] = useState<ProblemDetail | null>(null);
  const [conflictNotice, setConflictNotice] = useState(false);
  const [confirmTarget, setConfirmTarget] = useState<ComplianceControlStatus | null>(null);
  const [editOpen, setEditOpen] = useState(false);

  const refetch = useCallback(() => setRetries((n) => n + 1), []);

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);

    getComplianceControl(controlId, ctrl.signal)
      .then(setControl)
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(err instanceof ApiError ? err.problem : { title: 'Could not load compliance control', status: 0, detail: String(err) });
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });

    return () => ctrl.abort();
  }, [controlId, retries]);

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
    async (target: ComplianceControlStatus) => {
      if (!control) return;
      setBusy(true);
      setActionProblem(null);
      try {
        await setComplianceControlStatus(control.control_id, control.row_version, target);
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
    [control, afterMutation],
  );

  const onTransitionClick = (target: ComplianceControlStatus) => {
    setConflictNotice(false);
    if (CONSEQUENTIAL[target]) {
      setConfirmTarget(target);
      return;
    }
    void runTransition(target);
  };

  if (problem) return <ErrorState problem={problem} onRetry={refetch} />;

  if (loading && !control) {
    return (
      <Box color="text-body-secondary" padding="l">
        Loading compliance control…
      </Box>
    );
  }

  if (!control) return null;

  const transitions = legalComplianceControlTransitions(control.status);
  // Server order is oldest-first (ComplianceControlRepository.Evidence's own
  // doc comment); the console reads newest-first, the natural "what was
  // filed most recently" order for a trail an operator is actively adding
  // to.
  const evidenceNewestFirst = [...control.evidence].reverse();

  return (
    <SpaceBetween size="l">
      {conflictNotice && (
        <div role="alert">
          <Alert type="warning" header="Changed since you loaded it" dismissible onDismiss={() => setConflictNotice(false)}>
            This compliance control was modified concurrently. It has been reloaded to the current state below —
            review it and try again.
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
          { label: 'Framework', value: control.framework },
          { label: 'Control ref', value: control.control_ref },
          { label: 'Title', value: control.title },
          {
            label: 'Status',
            value: <StatusIndicator type={COMPLIANCE_CONTROL_STATUS_TYPE[control.status]}>{humanise(control.status)}</StatusIndicator>,
          },
          { label: 'Created', value: new Date(control.created_at).toLocaleString() },
          { label: 'Updated', value: new Date(control.updated_at).toLocaleString() },
        ]}
      />

      {control.description && (
        <Box>
          <Box variant="awsui-key-label">Description</Box>
          <Box>{control.description}</Box>
        </Box>
      )}

      <div>
        <Box variant="awsui-key-label" margin={{ bottom: 'xs' }}>
          Evidence trail
        </Box>
        {evidenceNewestFirst.length === 0 ? (
          <Box color="text-body-secondary">No evidence filed yet.</Box>
        ) : (
          <ol aria-label="Evidence trail" style={{ listStyle: 'none', margin: 0, padding: 0 }}>
            {evidenceNewestFirst.map((e) => (
              <EvidenceEntry key={e.evidence_id} evidence={e} />
            ))}
          </ol>
        )}
      </div>

      <AddEvidenceForm controlId={control.control_id} onAdded={refetch} />

      {editOpen && (
        <EditComplianceControlModal
          control={control}
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
          header={`${TRANSITION_LABEL[confirmTarget] ?? humanise(confirmTarget)} ${control.framework} ${control.control_ref}?`}
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
