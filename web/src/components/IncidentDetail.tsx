import { useCallback, useEffect, useRef, useState } from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import FormField from '@cloudscape-design/components/form-field';
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
  assignIncident,
  getIncident,
  getIncidentTimeline,
  legalTransitions,
  transitionIncident,
} from '../incidents';
import type { IncidentDTO, IncidentEventDTO, IncidentStatus } from '../incidents';
import { humanise, SEVERITY_TYPE, STATUS_TYPE } from '../incidentPresentation';
import { listUsers } from '../users';
import type { UserDTO } from '../users';
import { ErrorState } from './States';

/** Absolute time is the evidence; relative time is the convenience — mirrors AuditTimeline's own `when()`. */
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

function TimelineEntry({ event }: { event: IncidentEventDTO }) {
  const t = when(event.occurred_at);
  return (
    <li style={{ padding: '10px 0', borderBottom: '1px solid var(--awsui-color-border-divider-default, #e2e5ea)' }}>
      <SpaceBetween size="xxs">
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, flexWrap: 'wrap' }}>
          <Box variant="strong">{humanise(event.kind)}</Box>
          <Box>{event.actor}</Box>
          <time dateTime={event.occurred_at} title={t.absolute}>
            <Box color="text-body-secondary" fontSize="body-s" display="inline">
              {t.relative}
            </Box>
          </time>
        </div>
        {event.field && (
          <Box fontSize="body-s">
            <Box color="text-body-secondary" display="inline">
              {humanise(event.field)}
            </Box>{' '}
            {event.old_value ? (
              <>
                <s>{event.old_value}</s> <span aria-hidden="true">→</span>{' '}
              </>
            ) : null}
            <Box display="inline" fontWeight="bold">
              {event.new_value ?? '—'}
            </Box>
          </Box>
        )}
      </SpaceBetween>
    </li>
  );
}

/** Label for a transition BUTTON, keyed by its TARGET status. */
const TRANSITION_LABEL: Partial<Record<IncidentStatus, string>> = {
  acknowledged: 'Acknowledge',
  investigating: 'Start investigating',
  resolved: 'Resolve',
  closed: 'Close',
  reopened: 'Reopen',
};

/**
 * Consequential transitions (ADR-ACT-001 §1): confirmed via `Modal` before
 * sending, exactly as the brief specifies for resolve/close. Every other
 * legal transition (acknowledge, start investigating, reopen) fires directly
 * — still disabled-while-pending and still error-surfacing, just without an
 * extra confirmation click for a routine triage move.
 */
const CONSEQUENTIAL: Partial<Record<IncidentStatus, string>> = {
  resolved: 'This marks the incident as resolved. It can still be reopened later if the fix does not hold.',
  closed: 'This closes the incident. A closed incident is terminal — it cannot be reopened; a recurrence is filed as a new incident.',
};

/** The Cloudscape `Select` option for "no assignee" — value must be a string, never `null`, per SelectProps. */
const UNASSIGNED_OPTION: SelectProps.Option = { value: '', label: 'Unassigned' };

/**
 * The assignee control (ADR-ACT-001 §2). Tries the platform user directory
 * for a proper `Select`; on ANY load failure (403 is the expected one for a
 * tenant-scoped operator — see users.ts's doc comment on the confirmed
 * `requirePlatformAdmin` gap) degrades to a free-text user-id `Input` so
 * assignment still works, honestly labelled rather than silently broken.
 */
function AssigneeControl({
  incident,
  busy,
  onAssign,
}: {
  incident: IncidentDTO;
  busy: boolean;
  onAssign: (assigneeUserId: string | null) => void;
}) {
  const [users, setUsers] = useState<UserDTO[] | null>(null);
  const [directoryFailed, setDirectoryFailed] = useState(false);
  const [selected, setSelected] = useState(incident.assignee_user_id ?? '');

  useEffect(() => {
    setSelected(incident.assignee_user_id ?? '');
  }, [incident.incident_id, incident.assignee_user_id]);

  useEffect(() => {
    const ctrl = new AbortController();
    listUsers(200, ctrl.signal)
      .then((page) => setUsers(page.items ?? []))
      .catch(() => {
        if (!ctrl.signal.aborted) setDirectoryFailed(true);
      });
    return () => ctrl.abort();
  }, []);

  const unchanged = selected === (incident.assignee_user_id ?? '');

  if (users && !directoryFailed) {
    const options: SelectProps.Option[] = [
      UNASSIGNED_OPTION,
      ...users.map((u) => ({ value: u.user_id, label: `${u.display_name} (${u.email})` })),
    ];
    const selectedOption = options.find((o) => o.value === selected) ?? { value: selected, label: selected };
    return (
      <SpaceBetween direction="horizontal" size="xs" alignItems="end">
        <FormField label="Assignee">
          <Select
            selectedOption={selectedOption}
            onChange={({ detail }) => setSelected(detail.selectedOption.value ?? '')}
            options={options}
            disabled={busy}
            ariaLabel="Assignee"
            filteringType="auto"
          />
        </FormField>
        <Button disabled={busy || unchanged} loading={busy} onClick={() => onAssign(selected || null)}>
          Assign
        </Button>
      </SpaceBetween>
    );
  }

  return (
    <SpaceBetween direction="horizontal" size="xs" alignItems="end">
      <FormField
        label="Assignee"
        description={directoryFailed ? 'User directory unavailable here — enter a user id directly.' : undefined}
      >
        <Input
          value={selected}
          onChange={({ detail }) => setSelected(detail.value)}
          disabled={busy}
          placeholder="user id (blank to unassign)"
          ariaLabel="Assignee user id"
        />
      </FormField>
      <Button disabled={busy || unchanged} loading={busy} onClick={() => onAssign(selected.trim() || null)}>
        Assign
      </Button>
    </SpaceBetween>
  );
}

/**
 * The `SplitPanel` content for one incident: its detail fields, its
 * append-only timeline (E5.1), and — new in E-ACT.1 — the write actions an
 * operator performs from here (ADR-ACT-001): legal-only status transitions,
 * assignment, both under optimistic locking with 409-triggers-refetch
 * discipline. `onChanged` lets the board (or any other host) refresh its own
 * list once a mutation here has actually landed server-side.
 */
export function IncidentDetailPanel({
  incidentId,
  onChanged,
}: {
  incidentId: string;
  onChanged?: () => void;
}) {
  const [incident, setIncident] = useState<IncidentDTO | null>(null);
  const [events, setEvents] = useState<IncidentEventDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [retries, setRetries] = useState(0);
  const prevIncidentId = useRef<string | null>(null);

  // Mutation state. `conflictNotice` is distinct from `actionProblem`: a 409
  // is not an error to dwell on, it is a signal to look at the (already
  // refetched) current state and decide again — never a silent retry.
  const [busy, setBusy] = useState(false);
  const [actionProblem, setActionProblem] = useState<ProblemDetail | null>(null);
  const [conflictNotice, setConflictNotice] = useState(false);
  const [confirmTarget, setConfirmTarget] = useState<IncidentStatus | null>(null);

  const refetch = useCallback(() => setRetries((n) => n + 1), []);

  useEffect(() => {
    const ctrl = new AbortController();
    const idChanged = prevIncidentId.current !== incidentId;
    prevIncidentId.current = incidentId;

    setLoading(true);
    setProblem(null);
    // A same-id refetch (post-mutation, or after a 409) must not blank the
    // detail already on screen — only switching to a DIFFERENT incident
    // clears it, the same "don't blank what's already loaded" rule the
    // timeline fetch below follows for its own, narrower case.
    if (idChanged) {
      setIncident(null);
      setEvents([]);
    }

    getIncident(incidentId, ctrl.signal)
      .then((inc) => {
        setIncident(inc);
        // The timeline is supplementary — a failure there must not blank the
        // detail the caller already has.
        void getIncidentTimeline(incidentId, ctrl.signal)
          .then((t) => setEvents(t.items ?? []))
          .catch(() => {});
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load incident', status: 0, detail: String(err) },
        );
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });

    return () => ctrl.abort();
  }, [incidentId, retries]);

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
    async (target: IncidentStatus) => {
      if (!incident) return;
      setBusy(true);
      setActionProblem(null);
      try {
        await transitionIncident(incident.incident_id, target, incident.row_version);
        setConfirmTarget(null);
        afterMutation('ok');
      } catch (err) {
        setConfirmTarget(null);
        if (err instanceof ApiError && err.problem.status === 409) {
          afterMutation('conflict');
        } else {
          setActionProblem(
            err instanceof ApiError
              ? err.problem
              : { title: 'Transition failed', status: 0, detail: String(err) },
          );
        }
      } finally {
        setBusy(false);
      }
    },
    [incident, afterMutation],
  );

  const runAssign = useCallback(
    async (assigneeUserId: string | null) => {
      if (!incident) return;
      setBusy(true);
      setActionProblem(null);
      try {
        await assignIncident(incident.incident_id, assigneeUserId, incident.row_version);
        afterMutation('ok');
      } catch (err) {
        if (err instanceof ApiError && err.problem.status === 409) {
          afterMutation('conflict');
        } else {
          setActionProblem(
            err instanceof ApiError
              ? err.problem
              : { title: 'Assign failed', status: 0, detail: String(err) },
          );
        }
      } finally {
        setBusy(false);
      }
    },
    [incident, afterMutation],
  );

  const onTransitionClick = (target: IncidentStatus) => {
    setConflictNotice(false);
    if (CONSEQUENTIAL[target]) {
      setConfirmTarget(target);
      return;
    }
    void runTransition(target);
  };

  if (problem) return <ErrorState problem={problem} onRetry={refetch} />;

  if (loading && !incident) {
    return (
      <Box color="text-body-secondary" padding="l">
        Loading incident…
      </Box>
    );
  }

  if (!incident) return null;

  const transitions = legalTransitions(incident.status);

  return (
    <SpaceBetween size="l">
      {conflictNotice && (
        <div role="alert">
          <Alert type="warning" header="Changed since you loaded it" dismissible onDismiss={() => setConflictNotice(false)}>
            This incident was modified concurrently. It has been reloaded to the current state below — review it and try
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
        {transitions.map((target) => (
          <Button
            key={target}
            disabled={busy}
            loading={busy && confirmTarget === null && !CONSEQUENTIAL[target]}
            onClick={() => onTransitionClick(target)}
          >
            {TRANSITION_LABEL[target] ?? humanise(target)}
          </Button>
        ))}
        {transitions.length === 0 && (
          <Box color="text-body-secondary" fontSize="body-s">
            Closed is terminal — no further transition is offered.
          </Box>
        )}
      </SpaceBetween>

      <AssigneeControl incident={incident} busy={busy} onAssign={(id) => void runAssign(id)} />

      <KeyValuePairs
        columns={2}
        items={[
          { label: 'Severity', value: <StatusIndicator type={SEVERITY_TYPE[incident.severity]}>{humanise(incident.severity)}</StatusIndicator> },
          { label: 'Status', value: <StatusIndicator type={STATUS_TYPE[incident.status]}>{humanise(incident.status)}</StatusIndicator> },
          { label: 'Asset', value: incident.asset_id ?? <Box color="text-body-secondary">Unlinked</Box> },
          { label: 'Assignee', value: incident.assignee_user_id ?? <Box color="text-body-secondary">Unassigned</Box> },
          { label: 'Source', value: humanise(incident.source) },
          {
            label: 'Grouping',
            value: incident.root_incident_id
              ? `Collateral of ${incident.root_incident_id}`
              : 'Root / standalone',
          },
          { label: 'Created', value: new Date(incident.created_at).toLocaleString() },
          { label: 'Updated', value: new Date(incident.updated_at).toLocaleString() },
        ]}
      />

      {incident.description && (
        <Box>
          <Box variant="awsui-key-label">Description</Box>
          <Box>{incident.description}</Box>
        </Box>
      )}

      <div>
        <Box variant="awsui-key-label" margin={{ bottom: 'xs' }}>
          Timeline
        </Box>
        {events.length === 0 ? (
          <Box color="text-body-secondary">No timeline events yet.</Box>
        ) : (
          <ol style={{ listStyle: 'none', margin: 0, padding: 0 }}>
            {events.map((e) => (
              <TimelineEntry key={e.event_id} event={e} />
            ))}
          </ol>
        )}
      </div>

      {confirmTarget && (
        <Modal
          visible
          header={`${TRANSITION_LABEL[confirmTarget] ?? humanise(confirmTarget)} ${incident.incident_id}?`}
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
