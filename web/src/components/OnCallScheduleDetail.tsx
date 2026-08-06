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
import { humanise } from '../incidentPresentation';
import { combineDateAndTime } from '../maintenanceWindows';
import {
  ON_CALL_SCHEDULE_STATUSES,
  addOnCallParticipant,
  getOnCallNow,
  getOnCallSchedule,
  listOnCallParticipants,
  patchOnCallSchedule,
  presetForHandoffSeconds,
  removeOnCallParticipant,
  reorderOnCallParticipants,
} from '../onCall';
import type { OnCallNowDTO, OnCallParticipantDTO, OnCallScheduleDTO, OnCallScheduleStatus } from '../onCall';
import { ON_CALL_STATUS_TYPE, formatDurationSeconds } from '../onCallPresentation';
import { listTenantUsers } from '../users';
import type { TenantUserDTO } from '../users';
import { OnCallScheduleFields, resolveHandoffIntervalSeconds } from './OnCallScheduleForm';
import type { OnCallScheduleFieldValues } from './OnCallScheduleForm';
import { ErrorState } from './States';

const pad = (n: number) => String(n).padStart(2, '0');
/** Local wall-clock date/time strings for a Date, the inverse of maintenanceWindows.ts' combineDateAndTime — together they round-trip an existing instant through the same date+time inputs the create form uses. */
function toLocalDateInputValue(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}
function toLocalTimeInputValue(d: Date): string {
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

const STATUS_OPTIONS: SelectProps.Option[] = ON_CALL_SCHEDULE_STATUSES.map((s) => ({ value: s, label: humanise(s) }));

/**
 * "Edit schedule" (E-ACT.4): the same three fields the create modal uses,
 * pre-filled from the schedule just read, plus a status Select only PATCH can
 * change. PATCHed with the row_version that same read carried; a 409 is
 * reported to the caller (onConflict) rather than handled here, so the panel
 * can refetch and show the reload banner ADR-ACT-001 established.
 */
function EditOnCallScheduleModal({
  schedule,
  onClose,
  onSaved,
  onConflict,
}: {
  schedule: OnCallScheduleDTO;
  onClose: () => void;
  onSaved: () => void;
  onConflict: () => void;
}) {
  const start = new Date(schedule.rotation_start_at);
  const [fields, setFields] = useState<OnCallScheduleFieldValues>({
    name: schedule.name,
    handoffPreset: presetForHandoffSeconds(schedule.handoff_interval_seconds),
    handoffCustomSeconds: String(schedule.handoff_interval_seconds),
    rotationStartDate: toLocalDateInputValue(start),
    rotationStartTime: toLocalTimeInputValue(start),
  });
  const [status, setStatus] = useState<OnCallScheduleStatus>(schedule.status);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const trimmedName = fields.name.trim();
  const handoffSeconds = resolveHandoffIntervalSeconds(fields);
  const rotationStartAt = combineDateAndTime(fields.rotationStartDate, fields.rotationStartTime);
  const canSubmit =
    trimmedName.length > 0 &&
    trimmedName.length <= 200 &&
    handoffSeconds !== undefined &&
    handoffSeconds >= 1 &&
    Boolean(rotationStartAt) &&
    !busy;

  const submit = useCallback(async () => {
    if (!canSubmit || handoffSeconds === undefined || !rotationStartAt) return;
    setBusy(true);
    setProblem(null);
    try {
      await patchOnCallSchedule(
        schedule.schedule_id,
        {
          name: trimmedName,
          handoff_interval_seconds: handoffSeconds,
          rotation_start_at: rotationStartAt.toISOString(),
          status,
        },
        schedule.row_version,
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
  }, [canSubmit, handoffSeconds, rotationStartAt, schedule, trimmedName, status, onSaved, onConflict]);

  return (
    <Modal
      visible
      header={`Edit ${schedule.name}`}
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
              <Alert type="error" header="Could not update the schedule">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <OnCallScheduleFields values={fields} onChange={setFields} disabled={busy} />
          <FormField label="Status">
            <Select
              selectedOption={STATUS_OPTIONS.find((o) => o.value === status) ?? STATUS_OPTIONS[0]}
              onChange={({ detail }) => setStatus((detail.selectedOption.value ?? 'active') as OnCallScheduleStatus)}
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
 * The user picker for "add participant" (E-ACT.4), backed by
 * `GET /v1/admin/tenant-users` (ADR-ACT-003) — the payoff of E-ACT.0. Already-
 * rostered users are excluded from the options so a duplicate add (409,
 * `domain.ErrConflict`) is impossible to submit rather than merely surfaced
 * after the fact. Degrades to a free-text user-id `Input` on any directory
 * load failure, the same posture `AssigneeControl` uses.
 */
function AddParticipantControl({
  existingUserIds,
  busy,
  onAdd,
}: {
  existingUserIds: Set<string>;
  busy: boolean;
  onAdd: (userId: string) => void;
}) {
  const [users, setUsers] = useState<TenantUserDTO[] | null>(null);
  const [directoryFailed, setDirectoryFailed] = useState(false);
  const [selected, setSelected] = useState('');

  useEffect(() => {
    const ctrl = new AbortController();
    listTenantUsers(200, ctrl.signal)
      .then((page) => setUsers(page.items ?? []))
      .catch(() => {
        if (!ctrl.signal.aborted) setDirectoryFailed(true);
      });
    return () => ctrl.abort();
  }, []);

  if (users && !directoryFailed) {
    const options: SelectProps.Option[] = users
      .filter((u) => !existingUserIds.has(u.user_id))
      .map((u) => ({ value: u.user_id, label: `${u.display_name} (${u.email})` }));
    const selectedOption = options.find((o) => o.value === selected) ?? null;
    return (
      <SpaceBetween direction="horizontal" size="xs" alignItems="end">
        <FormField label="Add participant">
          <Select
            selectedOption={selectedOption}
            onChange={({ detail }) => setSelected(detail.selectedOption.value ?? '')}
            options={options}
            disabled={busy || options.length === 0}
            placeholder={options.length === 0 ? 'Every active tenant user is already rostered' : 'Choose a user'}
            ariaLabel="Add participant"
            filteringType="auto"
          />
        </FormField>
        <Button disabled={busy || !selected} loading={busy} onClick={() => onAdd(selected)}>
          Add
        </Button>
      </SpaceBetween>
    );
  }

  return (
    <SpaceBetween direction="horizontal" size="xs" alignItems="end">
      <FormField
        label="Add participant"
        description={directoryFailed ? 'User directory unavailable here — enter a user id directly.' : undefined}
      >
        <Input
          value={selected}
          onChange={({ detail }) => setSelected(detail.value)}
          disabled={busy}
          placeholder="user id"
          ariaLabel="Add participant user id"
        />
      </FormField>
      <Button disabled={busy || !selected.trim()} loading={busy} onClick={() => onAdd(selected.trim())}>
        Add
      </Button>
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
 * The `SplitPanel` content for one on-call schedule (E-ACT.4): its fields,
 * Edit, and full roster management (add via the tenant-users picker, remove
 * with confirmation, reorder via up/down — sent as ReorderParticipants' full
 * ordered set, never an interleaved partial move). `onChanged` refreshes the
 * host board (which shows who's-on-call-now) after any mutation here lands.
 */
export function OnCallScheduleDetailPanel({
  scheduleId,
  onChanged,
}: {
  scheduleId: string;
  onChanged?: () => void;
}) {
  const [schedule, setSchedule] = useState<OnCallScheduleDTO | null>(null);
  const [onCallNow, setOnCallNow] = useState<OnCallNowDTO | null>(null);
  const [participants, setParticipants] = useState<OnCallParticipantDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [retries, setRetries] = useState(0);

  const [busy, setBusy] = useState(false);
  const [actionProblem, setActionProblem] = useState<ProblemDetail | null>(null);
  const [conflictNotice, setConflictNotice] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [removeTarget, setRemoveTarget] = useState<OnCallParticipantDTO | null>(null);

  const refetch = useCallback(() => setRetries((n) => n + 1), []);

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);

    getOnCallSchedule(scheduleId, ctrl.signal)
      .then((sch) => {
        setSchedule(sch);
        void getOnCallNow(scheduleId, ctrl.signal)
          .then(setOnCallNow)
          .catch(() => {});
        void listOnCallParticipants(scheduleId, ctrl.signal)
          .then((page) => setParticipants(page.items ?? []))
          .catch(() => {});
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load on-call schedule', status: 0, detail: String(err) },
        );
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });

    return () => ctrl.abort();
  }, [scheduleId, retries]);

  /** Runs after edit/add/remove/reorder: success and (edit's own) 409 both refetch; only other errors leave state as-is. */
  const afterMutation = useCallback(
    (outcome: 'ok' | 'conflict') => {
      setConflictNotice(outcome === 'conflict');
      refetch();
      onChanged?.();
    },
    [refetch, onChanged],
  );

  const orderedParticipants = [...participants].sort((a, b) => a.position - b.position);
  const existingUserIds = new Set(orderedParticipants.map((p) => p.user_id));

  const runAdd = useCallback(
    async (userId: string) => {
      if (!schedule) return;
      setBusy(true);
      setActionProblem(null);
      try {
        await addOnCallParticipant(schedule.schedule_id, userId);
        afterMutation('ok');
      } catch (err) {
        // A 409 here means "already rostered" (domain.ErrConflict) — a
        // business-rule conflict, not a stale-read one, so it is surfaced as
        // an ordinary inline error rather than the row_version reload banner
        // (onCall.ts' top-of-file contract note).
        setActionProblem(err instanceof ApiError ? err.problem : { title: 'Add failed', status: 0, detail: String(err) });
      } finally {
        setBusy(false);
      }
    },
    [schedule, afterMutation],
  );

  const runRemove = useCallback(async () => {
    if (!schedule || !removeTarget) return;
    setBusy(true);
    setActionProblem(null);
    try {
      await removeOnCallParticipant(schedule.schedule_id, removeTarget.participant_id);
      setRemoveTarget(null);
      afterMutation('ok');
    } catch (err) {
      setRemoveTarget(null);
      setActionProblem(err instanceof ApiError ? err.problem : { title: 'Remove failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [schedule, removeTarget, afterMutation]);

  const runReorder = useCallback(
    async (index: number, direction: -1 | 1) => {
      if (!schedule) return;
      const newOrder = swapPosition(
        orderedParticipants.map((p) => p.participant_id),
        index,
        direction,
      );
      setBusy(true);
      setActionProblem(null);
      try {
        await reorderOnCallParticipants(schedule.schedule_id, newOrder);
        afterMutation('ok');
      } catch (err) {
        setActionProblem(err instanceof ApiError ? err.problem : { title: 'Reorder failed', status: 0, detail: String(err) });
      } finally {
        setBusy(false);
      }
    },
    [schedule, orderedParticipants, afterMutation],
  );

  if (problem) return <ErrorState problem={problem} onRetry={refetch} />;

  if (loading && !schedule) {
    return (
      <Box color="text-body-secondary" padding="l">
        Loading on-call schedule…
      </Box>
    );
  }

  if (!schedule) return null;

  return (
    <SpaceBetween size="l">
      {conflictNotice && (
        <div role="alert">
          <Alert type="warning" header="Changed since you loaded it" dismissible onDismiss={() => setConflictNotice(false)}>
            This schedule was modified concurrently. It has been reloaded to the current state below — review it and try
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
          { label: 'Name', value: schedule.name },
          { label: 'Status', value: <StatusIndicator type={ON_CALL_STATUS_TYPE[schedule.status]}>{humanise(schedule.status)}</StatusIndicator> },
          { label: 'Handoff interval', value: formatDurationSeconds(schedule.handoff_interval_seconds) },
          { label: 'Rotation start', value: new Date(schedule.rotation_start_at).toLocaleString() },
          {
            label: 'On call now',
            value: onCallNow?.display_name ?? onCallNow?.user_id ?? <Box color="text-body-secondary">Unassigned</Box>,
          },
          { label: 'Created', value: new Date(schedule.created_at).toLocaleString() },
          { label: 'Updated', value: new Date(schedule.updated_at).toLocaleString() },
        ]}
      />

      <Header variant="h3" counter={`(${orderedParticipants.length})`}>
        Roster (rotation order)
      </Header>

      {orderedParticipants.length === 0 && <Box color="text-body-secondary">No participants yet.</Box>}

      <SpaceBetween size="xs">
        {orderedParticipants.map((p, idx) => (
          <SpaceBetween key={p.participant_id} direction="horizontal" size="xs" alignItems="center">
            <Box>{idx + 1}.</Box>
            <Box>{p.user_id}</Box>
            {p.user_id === onCallNow?.user_id && <StatusIndicator type="success">on call now</StatusIndicator>}
            <Button
              iconName="angle-up"
              variant="icon"
              ariaLabel={`Move ${p.user_id} up`}
              disabled={busy || idx === 0}
              onClick={() => void runReorder(idx, -1)}
            />
            <Button
              iconName="angle-down"
              variant="icon"
              ariaLabel={`Move ${p.user_id} down`}
              disabled={busy || idx === orderedParticipants.length - 1}
              onClick={() => void runReorder(idx, 1)}
            />
            <Button
              iconName="remove"
              variant="icon"
              ariaLabel={`Remove ${p.user_id}`}
              disabled={busy}
              onClick={() => setRemoveTarget(p)}
            />
          </SpaceBetween>
        ))}
      </SpaceBetween>

      <AddParticipantControl existingUserIds={existingUserIds} busy={busy} onAdd={(userId) => void runAdd(userId)} />

      {editOpen && (
        <EditOnCallScheduleModal
          schedule={schedule}
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
          header={`Remove ${removeTarget.user_id} from this schedule?`}
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
            This removes {removeTarget.user_id} from the rotation and closes the resulting gap in position. There is no
            optimistic-lock check on remove (the endpoint takes no row_version).
          </Box>
        </Modal>
      )}
    </SpaceBetween>
  );
}
