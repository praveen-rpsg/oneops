import { useCallback, useEffect, useState } from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Modal from '@cloudscape-design/components/modal';
import Select from '@cloudscape-design/components/select';
import type { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import {
  ESCALATION_POLICY_STATUSES,
  addEscalationTier,
  escalationWaitSecondsError,
  getEscalationPolicy,
  listEscalationTiers,
  patchEscalationPolicy,
  removeEscalationTier,
  reorderEscalationTiers,
  ESCALATION_WAIT_PRESETS,
} from '../escalation';
import type { EscalationPolicyDTO, EscalationPolicyStatus, EscalationTierDTO } from '../escalation';
import { ESCALATION_POLICY_STATUS_TYPE } from '../escalationPresentation';
import { formatDurationSeconds } from '../onCallPresentation';
import { listOnCallSchedules } from '../onCall';
import type { OnCallScheduleDTO } from '../onCall';
import { humanise } from '../incidentPresentation';
import { EscalationPolicyFields } from './EscalationPolicyForm';
import type { EscalationPolicyFieldValues } from './EscalationPolicyForm';
import { ErrorState } from './States';

const STATUS_OPTIONS: SelectProps.Option[] = ESCALATION_POLICY_STATUSES.map((s) => ({ value: s, label: humanise(s) }));
const WAIT_OPTIONS: SelectProps.Option[] = ESCALATION_WAIT_PRESETS.map((p) => ({ value: p.value, label: p.label }));

/** The interval a set of preset+custom values actually names. undefined if unanswered/invalid. */
function resolveWaitSeconds(preset: string, custom: string): number | undefined {
  const found = ESCALATION_WAIT_PRESETS.find((p) => p.value === preset);
  if (found?.seconds !== undefined) return found.seconds;
  const n = Number(custom);
  return custom.trim() !== '' && Number.isFinite(n) ? n : undefined;
}

/**
 * "Edit policy" (E-ACT.5): the same name field the create modal uses,
 * pre-filled from the policy just read, plus a status Select only PATCH can
 * change. PATCHed with the row_version that same read carried; a 409 is
 * reported to the caller (onConflict) rather than handled here, so the panel
 * can refetch and show the ADR-ACT-001 reload banner.
 */
function EditEscalationPolicyModal({
  policy,
  onClose,
  onSaved,
  onConflict,
}: {
  policy: EscalationPolicyDTO;
  onClose: () => void;
  onSaved: () => void;
  onConflict: () => void;
}) {
  const [fields, setFields] = useState<EscalationPolicyFieldValues>({ name: policy.name });
  const [status, setStatus] = useState<EscalationPolicyStatus>(policy.status);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const trimmedName = fields.name.trim();
  const canSubmit = trimmedName.length > 0 && trimmedName.length <= 200 && !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      await patchEscalationPolicy(policy.policy_id, { name: trimmedName, status }, policy.row_version);
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
  }, [canSubmit, policy, trimmedName, status, onSaved, onConflict]);

  return (
    <Modal
      visible
      header={`Edit ${policy.name}`}
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
              <Alert type="error" header="Could not update the policy">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <EscalationPolicyFields values={fields} onChange={setFields} disabled={busy} />
          <FormField label="Status">
            <Select
              selectedOption={STATUS_OPTIONS.find((o) => o.value === status) ?? STATUS_OPTIONS[0]}
              onChange={({ detail }) => setStatus((detail.selectedOption.value ?? 'active') as EscalationPolicyStatus)}
              options={STATUS_OPTIONS}
              disabled={busy}
              ariaLabel="Status"
            />
          </FormField>
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

/**
 * The tier picker for "add tier" (E-ACT.5): an on-call-schedule `Select`
 * (from `GET /v1/admin/on-call-schedules`, ACTIVE schedules only — the same
 * bound the on-call board itself applies to its own cards) plus a
 * wait-seconds field (friendly presets, falling back to raw seconds, the
 * same shape `OnCallScheduleFields`' handoff interval uses). Unlike
 * `AddParticipantControl`, this picker does NOT exclude schedules already
 * used by another tier of this policy: `escalation_tier` has no uniqueness
 * constraint on `on_call_schedule_id` (only on `(tenant_id, policy_id,
 * position)`, confirmed against the migration) — a policy may legitimately
 * page the same schedule at two different tiers. Degrades to a free-text
 * schedule-id `Input` on any directory load failure, the same posture
 * `AddParticipantControl`/`AssigneeControl` use.
 */
function AddTierControl({ busy, onAdd }: { busy: boolean; onAdd: (scheduleId: string, waitSeconds: number) => void }) {
  const [schedules, setSchedules] = useState<OnCallScheduleDTO[] | null>(null);
  const [directoryFailed, setDirectoryFailed] = useState(false);
  const [selectedScheduleId, setSelectedScheduleId] = useState('');
  const [waitPreset, setWaitPreset] = useState('300');
  const [waitCustom, setWaitCustom] = useState('');

  useEffect(() => {
    const ctrl = new AbortController();
    listOnCallSchedules(ctrl.signal)
      .then((page) => setSchedules((page.items ?? []).filter((s) => s.status === 'active')))
      .catch(() => {
        if (!ctrl.signal.aborted) setDirectoryFailed(true);
      });
    return () => ctrl.abort();
  }, []);

  const waitSeconds = resolveWaitSeconds(waitPreset, waitCustom);
  const waitError =
    waitPreset === 'custom' && waitCustom.trim() === ''
      ? undefined
      : waitSeconds === undefined
        ? undefined
        : escalationWaitSecondsError(waitSeconds);
  const canAdd = Boolean(selectedScheduleId.trim()) && waitSeconds !== undefined && waitSeconds >= 1 && !busy;

  const waitField = (
    <FormField label="Wait time" description="How long to wait for an acknowledgement before advancing." errorText={waitError}>
      <SpaceBetween direction="horizontal" size="xs">
        <Select
          selectedOption={WAIT_OPTIONS.find((o) => o.value === waitPreset) ?? WAIT_OPTIONS[0]}
          onChange={({ detail }) => setWaitPreset(detail.selectedOption.value ?? 'custom')}
          options={WAIT_OPTIONS}
          disabled={busy}
          ariaLabel="Wait time preset"
        />
        {waitPreset === 'custom' && (
          <Input
            type="number"
            value={waitCustom}
            onChange={({ detail }) => setWaitCustom(detail.value)}
            disabled={busy}
            ariaLabel="Wait time seconds"
            placeholder="Seconds"
          />
        )}
      </SpaceBetween>
    </FormField>
  );

  if (schedules && !directoryFailed) {
    const options: SelectProps.Option[] = schedules.map((s) => ({ value: s.schedule_id, label: s.name }));
    const selectedOption = options.find((o) => o.value === selectedScheduleId) ?? null;
    return (
      <SpaceBetween size="s">
        <SpaceBetween direction="horizontal" size="xs" alignItems="end">
          <FormField label="On-call schedule">
            <Select
              selectedOption={selectedOption}
              onChange={({ detail }) => setSelectedScheduleId(detail.selectedOption.value ?? '')}
              options={options}
              disabled={busy || options.length === 0}
              placeholder={options.length === 0 ? 'No active on-call schedules exist' : 'Choose a schedule'}
              ariaLabel="On-call schedule"
              filteringType="auto"
            />
          </FormField>
          {waitField}
          <Button
            disabled={!canAdd}
            loading={busy}
            onClick={() => canAdd && waitSeconds !== undefined && onAdd(selectedScheduleId, waitSeconds)}
          >
            Add tier
          </Button>
        </SpaceBetween>
      </SpaceBetween>
    );
  }

  return (
    <SpaceBetween size="s">
      <SpaceBetween direction="horizontal" size="xs" alignItems="end">
        <FormField
          label="On-call schedule"
          description={directoryFailed ? 'Schedule directory unavailable here — enter a schedule id directly.' : undefined}
        >
          <Input
            value={selectedScheduleId}
            onChange={({ detail }) => setSelectedScheduleId(detail.value)}
            disabled={busy}
            placeholder="on-call schedule id"
            ariaLabel="On-call schedule id"
          />
        </FormField>
        {waitField}
        <Button
          disabled={!canAdd}
          loading={busy}
          onClick={() => canAdd && waitSeconds !== undefined && onAdd(selectedScheduleId.trim(), waitSeconds)}
        >
          Add tier
        </Button>
      </SpaceBetween>
    </SpaceBetween>
  );
}

function swapPosition(orderedIds: string[], index: number, direction: -1 | 1): string[] {
  const target = index + direction;
  if (target < 0 || target >= orderedIds.length) return orderedIds;
  const next = [...orderedIds];
  [next[index], next[target]] = [next[target], next[index]];
  return next;
}

/**
 * The `SplitPanel` content for one escalation policy (E-ACT.5): its fields,
 * Edit, and full tier-ladder management (add via the on-call-schedule
 * picker, remove with confirmation, reorder via up/down — sent as
 * ReorderTiers' full ordered set, never an interleaved partial move) — the
 * identical structure `OnCallScheduleDetailPanel` (ADR-ACT-005) established
 * for a schedule's roster. `onChanged` refreshes the host board after any
 * mutation here lands.
 */
export function EscalationPolicyDetailPanel({
  policyId,
  onChanged,
}: {
  policyId: string;
  onChanged?: () => void;
}) {
  const [policy, setPolicy] = useState<EscalationPolicyDTO | null>(null);
  const [tiers, setTiers] = useState<EscalationTierDTO[]>([]);
  const [schedulesById, setSchedulesById] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [retries, setRetries] = useState(0);

  const [busy, setBusy] = useState(false);
  const [actionProblem, setActionProblem] = useState<ProblemDetail | null>(null);
  const [conflictNotice, setConflictNotice] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [removeTarget, setRemoveTarget] = useState<EscalationTierDTO | null>(null);

  const refetch = useCallback(() => setRetries((n) => n + 1), []);

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);

    getEscalationPolicy(policyId, ctrl.signal)
      .then((p) => {
        setPolicy(p);
        void listEscalationTiers(policyId, ctrl.signal)
          .then((page) => setTiers(page.items ?? []))
          .catch(() => {});
        void listOnCallSchedules(ctrl.signal)
          .then((page) => {
            const map: Record<string, string> = {};
            (page.items ?? []).forEach((s) => (map[s.schedule_id] = s.name));
            setSchedulesById(map);
          })
          .catch(() => {});
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load escalation policy', status: 0, detail: String(err) },
        );
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });

    return () => ctrl.abort();
  }, [policyId, retries]);

  /** Runs after edit/add/remove/reorder: success and (edit's own) 409 both refetch; only other errors leave state as-is. */
  const afterMutation = useCallback(
    (outcome: 'ok' | 'conflict') => {
      setConflictNotice(outcome === 'conflict');
      refetch();
      onChanged?.();
    },
    [refetch, onChanged],
  );

  const orderedTiers = [...tiers].sort((a, b) => a.position - b.position);

  const runAdd = useCallback(
    async (scheduleId: string, waitSeconds: number) => {
      if (!policy) return;
      setBusy(true);
      setActionProblem(null);
      try {
        await addEscalationTier(policy.policy_id, scheduleId, waitSeconds);
        afterMutation('ok');
      } catch (err) {
        // A 409 here means "tier could not be added" (domain.ErrConflict) —
        // a business-rule conflict, not a stale-read one (escalation.ts' top-
        // of-file contract note, ADR-ACT-005 Decision 3's identical split),
        // so it is surfaced as an ordinary inline error rather than the
        // row_version reload banner.
        setActionProblem(err instanceof ApiError ? err.problem : { title: 'Add tier failed', status: 0, detail: String(err) });
      } finally {
        setBusy(false);
      }
    },
    [policy, afterMutation],
  );

  const runRemove = useCallback(async () => {
    if (!policy || !removeTarget) return;
    setBusy(true);
    setActionProblem(null);
    try {
      await removeEscalationTier(policy.policy_id, removeTarget.tier_id);
      setRemoveTarget(null);
      afterMutation('ok');
    } catch (err) {
      setRemoveTarget(null);
      setActionProblem(err instanceof ApiError ? err.problem : { title: 'Remove tier failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [policy, removeTarget, afterMutation]);

  const runReorder = useCallback(
    async (index: number, direction: -1 | 1) => {
      if (!policy) return;
      const newOrder = swapPosition(
        orderedTiers.map((t) => t.tier_id),
        index,
        direction,
      );
      setBusy(true);
      setActionProblem(null);
      try {
        await reorderEscalationTiers(policy.policy_id, newOrder);
        afterMutation('ok');
      } catch (err) {
        setActionProblem(err instanceof ApiError ? err.problem : { title: 'Reorder failed', status: 0, detail: String(err) });
      } finally {
        setBusy(false);
      }
    },
    [policy, orderedTiers, afterMutation],
  );

  if (problem) return <ErrorState problem={problem} onRetry={refetch} />;

  if (loading && !policy) {
    return (
      <Box color="text-body-secondary" padding="l">
        Loading escalation policy…
      </Box>
    );
  }

  if (!policy) return null;

  return (
    <SpaceBetween size="l">
      {conflictNotice && (
        <div role="alert">
          <Alert type="warning" header="Changed since you loaded it" dismissible onDismiss={() => setConflictNotice(false)}>
            This policy was modified concurrently. It has been reloaded to the current state below — review it and try
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
      </SpaceBetween>

      <KeyValuePairs
        columns={2}
        items={[
          { label: 'Name', value: policy.name },
          {
            label: 'Status',
            value: <StatusIndicator type={ESCALATION_POLICY_STATUS_TYPE[policy.status]}>{humanise(policy.status)}</StatusIndicator>,
          },
          { label: 'Created', value: new Date(policy.created_at).toLocaleString() },
          { label: 'Updated', value: new Date(policy.updated_at).toLocaleString() },
        ]}
      />

      <Header variant="h3" counter={`(${orderedTiers.length})`}>
        Escalation ladder (tier order)
      </Header>

      {orderedTiers.length === 0 && <Box color="text-body-secondary">No tiers yet.</Box>}

      <SpaceBetween size="xs">
        {orderedTiers.map((t, idx) => (
          <SpaceBetween key={t.tier_id} direction="horizontal" size="xs" alignItems="center">
            <Box>{idx + 1}.</Box>
            <Box>{schedulesById[t.on_call_schedule_id] ?? t.on_call_schedule_id}</Box>
            <Box color="text-body-secondary">wait {formatDurationSeconds(t.wait_seconds)}</Box>
            <Button
              iconName="angle-up"
              variant="icon"
              ariaLabel={`Move tier ${idx + 1} up`}
              disabled={busy || idx === 0}
              onClick={() => void runReorder(idx, -1)}
            />
            <Button
              iconName="angle-down"
              variant="icon"
              ariaLabel={`Move tier ${idx + 1} down`}
              disabled={busy || idx === orderedTiers.length - 1}
              onClick={() => void runReorder(idx, 1)}
            />
            <Button
              iconName="remove"
              variant="icon"
              ariaLabel={`Remove tier ${idx + 1}`}
              disabled={busy}
              onClick={() => setRemoveTarget(t)}
            />
          </SpaceBetween>
        ))}
      </SpaceBetween>

      <AddTierControl busy={busy} onAdd={(scheduleId, waitSeconds) => void runAdd(scheduleId, waitSeconds)} />

      {editOpen && (
        <EditEscalationPolicyModal
          policy={policy}
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

      {removeTarget && (
        <Modal
          visible
          header={`Remove tier ${orderedTiers.findIndex((t) => t.tier_id === removeTarget.tier_id) + 1}?`}
          onDismiss={() => {
            if (!busy) setRemoveTarget(null);
          }}
          closeAriaLabel="Dismiss"
          footer={
            <Box float="right">
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="link" onClick={() => setRemoveTarget(null)} disabled={busy}>
                  Cancel
                </Button>
                <Button variant="primary" loading={busy} onClick={() => void runRemove()}>
                  Remove
                </Button>
              </SpaceBetween>
            </Box>
          }
        >
          <Box>
            This removes {schedulesById[removeTarget.on_call_schedule_id] ?? removeTarget.on_call_schedule_id} from
            the ladder and closes the resulting gap in position. There is no optimistic-lock check on remove (the
            endpoint takes no row_version).
          </Box>
        </Modal>
      )}
    </SpaceBetween>
  );
}
