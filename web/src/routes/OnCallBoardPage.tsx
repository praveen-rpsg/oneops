import { useCallback, useEffect, useState } from 'react';
import { useOutletContext } from 'react-router-dom';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Cards from '@cloudscape-design/components/cards';
import type { CardsProps } from '@cloudscape-design/components/cards';
import Form from '@cloudscape-design/components/form';
import Header from '@cloudscape-design/components/header';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { OnCallScheduleDetailPanel } from '../components/OnCallScheduleDetail';
import { OnCallScheduleFields, resolveHandoffIntervalSeconds } from '../components/OnCallScheduleForm';
import type { OnCallScheduleFieldValues } from '../components/OnCallScheduleForm';
import { humanise } from '../incidentPresentation';
import { combineDateAndTime } from '../maintenanceWindows';
import {
  ON_CALL_DETAIL_FETCH_CAP,
  ON_CALL_SCHEDULE_LIST_CAP,
  createOnCallSchedule,
  getOnCallNow,
  listOnCallParticipants,
  listOnCallSchedules,
} from '../onCall';
import type { OnCallNowDTO, OnCallParticipantDTO, OnCallScheduleDTO } from '../onCall';
import { ON_CALL_STATUS_TYPE, formatDurationSeconds } from '../onCallPresentation';
import { ErrorState } from '../components/States';
import type { ShellSplitPanelContext } from '../Shell';

/** Per-schedule supplementary fetch outcome — undefined means "not fetched yet or beyond the cap". */
interface ScheduleDetail {
  onCall?: OnCallNowDTO;
  onCallFailed?: boolean;
  participants?: OnCallParticipantDTO[];
  participantsFailed?: boolean;
}

function OnCallNowValue({ detail, fetched }: { detail?: ScheduleDetail; fetched: boolean }) {
  if (!fetched) {
    return <Box color="text-body-secondary">Not loaded (see the fetch bound below)</Box>;
  }
  if (!detail || detail.onCallFailed) {
    return <Box color="text-status-error">Could not load</Box>;
  }
  const label = detail.onCall?.display_name ?? detail.onCall?.user_id;
  return label ? (
    <StatusIndicator type="in-progress">{label}</StatusIndicator>
  ) : (
    <Box color="text-body-secondary">Unassigned</Box>
  );
}

function ParticipantsList({ detail, fetched, onCallUserId }: { detail?: ScheduleDetail; fetched: boolean; onCallUserId?: string }) {
  if (!fetched) {
    return <Box color="text-body-secondary">Not loaded (see the fetch bound below)</Box>;
  }
  if (!detail || detail.participantsFailed) {
    return <Box color="text-status-error">Could not load</Box>;
  }
  const participants = detail.participants ?? [];
  if (participants.length === 0) {
    return <Box color="text-body-secondary">No participants yet.</Box>;
  }
  return (
    <ol style={{ margin: 0, paddingLeft: '1.25em' }}>
      {[...participants]
        .sort((a, b) => a.position - b.position)
        .map((p) => (
          <li key={p.participant_id}>
            {p.user_id}
            {p.user_id === onCallUserId && (
              <>
                {' '}
                <StatusIndicator type="success">on call now</StatusIndicator>
              </>
            )}
          </li>
        ))}
    </ol>
  );
}

/**
 * "Create schedule" (E-ACT.4): the same three fields the edit modal uses
 * (components/OnCallScheduleDetail.tsx) — name, handoff interval (a friendly
 * preset Select, falling back to raw seconds), rotation start (date+time,
 * sent as RFC3339). Mirrors createOnCallScheduleRequest field-for-field;
 * status is not collected — a freshly created schedule is always "active"
 * server-side (domain.NewOnCallSchedule), there is no caller-chosen initial
 * status to offer.
 */
function CreateOnCallScheduleModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (sch: OnCallScheduleDTO) => void;
}) {
  const [fields, setFields] = useState<OnCallScheduleFieldValues>({
    name: '',
    handoffPreset: '86400',
    handoffCustomSeconds: '',
    rotationStartDate: '',
    rotationStartTime: '',
  });
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
      const created = await createOnCallSchedule({
        name: trimmedName,
        handoff_interval_seconds: handoffSeconds,
        rotation_start_at: rotationStartAt.toISOString(),
      });
      onCreated(created);
    } catch (err) {
      setProblem(err instanceof ApiError ? err.problem : { title: 'Create failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [canSubmit, handoffSeconds, rotationStartAt, trimmedName, onCreated]);

  return (
    <Modal
      visible
      header="Create schedule"
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
              <Alert type="error" header="Could not create the schedule">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <OnCallScheduleFields values={fields} onChange={setFields} disabled={busy} />
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

function OnCallEmpty({ hasAnySchedules }: { hasAnySchedules: boolean }) {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>{hasAnySchedules ? 'No active on-call schedules.' : 'No on-call schedules have been created yet.'}</b>
        <Box variant="p" color="text-body-secondary">
          {hasAnySchedules
            ? 'Every schedule for this tenant is archived.'
            : 'Schedules and their rotations appear here once they are created through the API.'}
        </Box>
      </SpaceBetween>
    </Box>
  );
}

/**
 * The on-call board: every active on-call schedule for the caller's tenant,
 * with who is on call right now and the ordered rotation roster, over the
 * existing `GET /v1/admin/on-call-schedules{,/{id}/on-call,/{id}/
 * participants}` endpoints unchanged. The schedule list is one bounded fetch
 * (ON_CALL_SCHEDULE_LIST_CAP); the two supplementary per-schedule fetches are
 * additionally bounded (ON_CALL_DETAIL_FETCH_CAP) and run in parallel, never
 * blocking the schedule cards themselves from rendering.
 */
export function OnCallBoardPage() {
  const { openSplitPanel } = useOutletContext<ShellSplitPanelContext>();

  const [schedules, setSchedules] = useState<OnCallScheduleDTO[]>([]);
  const [details, setDetails] = useState<Record<string, ScheduleDetail>>({});
  const [fetchedIds, setFetchedIds] = useState<Set<string>>(new Set());
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);
  const [createOpen, setCreateOpen] = useState(false);

  const load = useCallback((signal: AbortSignal) => {
    setLoading(true);
    setProblem(null);
    listOnCallSchedules(signal)
      .then((page) => {
        const items = page.items ?? [];
        setSchedules(items);
        setDetails({});

        const active = items.filter((s) => s.status === 'active').slice(0, ON_CALL_DETAIL_FETCH_CAP);
        setFetchedIds(new Set(active.map((s) => s.schedule_id)));

        active.forEach((sch) => {
          getOnCallNow(sch.schedule_id, signal)
            .then((onCall) => {
              if (signal.aborted) return;
              setDetails((prev) => ({ ...prev, [sch.schedule_id]: { ...prev[sch.schedule_id], onCall } }));
            })
            .catch(() => {
              if (signal.aborted) return;
              setDetails((prev) => ({ ...prev, [sch.schedule_id]: { ...prev[sch.schedule_id], onCallFailed: true } }));
            });

          listOnCallParticipants(sch.schedule_id, signal)
            .then((res) => {
              if (signal.aborted) return;
              setDetails((prev) => ({
                ...prev,
                [sch.schedule_id]: { ...prev[sch.schedule_id], participants: res.items ?? [] },
              }));
            })
            .catch(() => {
              if (signal.aborted) return;
              setDetails((prev) => ({ ...prev, [sch.schedule_id]: { ...prev[sch.schedule_id], participantsFailed: true } }));
            });
        });
      })
      .catch((err: unknown) => {
        if (signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load on-call schedules', status: 0, detail: String(err) },
        );
        setSchedules([]);
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

  const activeSchedules = schedules.filter((s) => s.status === 'active');

  /** Reruns the board's own fetch — shared by error-retry, a successful create, and any edit/roster mutation from the detail panel. */
  const reload = useCallback(() => setReloads((n) => n + 1), []);

  const openSchedule = useCallback(
    (sch: OnCallScheduleDTO) => {
      openSplitPanel(
        sch.name,
        <OnCallScheduleDetailPanel scheduleId={sch.schedule_id} onChanged={reload} />,
      );
    },
    [openSplitPanel, reload],
  );

  const cardDefinition: CardsProps.CardDefinition<OnCallScheduleDTO> = {
    header: (sch) => (
      // Not wrapped in a heading-level Box: the SplitPanel this opens uses the
      // same name as ITS OWN heading (Shell's `SplitPanel header={...}`), so a
      // second `role="heading"` here would give the page two headings with the
      // identical accessible name while the panel is open.
      <SpaceBetween direction="horizontal" size="xs" alignItems="center">
        <Button variant="inline-link" onClick={() => openSchedule(sch)}>
          {sch.name}
        </Button>
        <StatusIndicator type={ON_CALL_STATUS_TYPE[sch.status]}>{humanise(sch.status)}</StatusIndicator>
      </SpaceBetween>
    ),
    sections: [
      {
        id: 'on-call-now',
        header: 'On call now',
        content: (sch) => <OnCallNowValue detail={details[sch.schedule_id]} fetched={fetchedIds.has(sch.schedule_id)} />,
      },
      {
        id: 'handoff-interval',
        header: 'Handoff interval',
        content: (sch) => formatDurationSeconds(sch.handoff_interval_seconds),
      },
      {
        id: 'participants',
        header: 'Participants (rotation order)',
        content: (sch) => (
          <ParticipantsList
            detail={details[sch.schedule_id]}
            fetched={fetchedIds.has(sch.schedule_id)}
            onCallUserId={details[sch.schedule_id]?.onCall?.user_id}
          />
        ),
      },
      {
        id: 'actions',
        content: (sch) => (
          <Button onClick={() => openSchedule(sch)}>Manage schedule</Button>
        ),
      },
    ],
  };

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Active on-call schedules, who is on call right now, and each rotation's roster."
        counter={`(${activeSchedules.length})`}
        actions={<Button onClick={() => setCreateOpen(true)}>Create schedule</Button>}
      >
        On-call
      </Header>

      {schedules.length >= ON_CALL_SCHEDULE_LIST_CAP && (
        <Box color="text-status-warning" fontSize="body-s">
          Showing the first {ON_CALL_SCHEDULE_LIST_CAP} schedules for this tenant.
        </Box>
      )}
      {activeSchedules.length > ON_CALL_DETAIL_FETCH_CAP && (
        <Box color="text-status-warning" fontSize="body-s">
          Only the first {ON_CALL_DETAIL_FETCH_CAP} active schedules load who's-on-call and roster detail; the rest
          show name and handoff interval only (see onCall.ts' ON_CALL_DETAIL_FETCH_CAP).
        </Box>
      )}

      {problem && <ErrorState problem={problem} onRetry={reload} />}

      {!problem && (
        <Cards
          items={activeSchedules}
          cardDefinition={cardDefinition}
          trackBy="schedule_id"
          loading={loading}
          loadingText="Loading on-call schedules"
          cardsPerRow={[{ cards: 1 }, { minWidth: 500, cards: 2 }, { minWidth: 900, cards: 3 }]}
          ariaLabels={{ itemSelectionLabel: () => '', selectionGroupLabel: 'On-call schedules' }}
          empty={<OnCallEmpty hasAnySchedules={schedules.length > 0} />}
        />
      )}

      {createOpen && (
        <CreateOnCallScheduleModal
          onClose={() => setCreateOpen(false)}
          onCreated={(created) => {
            setCreateOpen(false);
            reload();
            openSchedule(created);
          }}
        />
      )}
    </SpaceBetween>
  );
}
