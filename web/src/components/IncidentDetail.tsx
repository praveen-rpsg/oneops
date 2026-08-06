import { useEffect, useState } from 'react';
import Box from '@cloudscape-design/components/box';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { getIncident, getIncidentTimeline } from '../incidents';
import type { IncidentDTO, IncidentEventDTO } from '../incidents';
import { humanise, SEVERITY_TYPE, STATUS_TYPE } from '../incidentPresentation';
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

/**
 * The `SplitPanel` content for one incident: its detail fields plus its
 * append-only timeline (E5.1), both read from the existing incident
 * endpoints — no new data surface. Reuses the same load/error/empty
 * structure `ArtifactDetail`/`AuditTimeline` already establish.
 */
export function IncidentDetailPanel({ incidentId }: { incidentId: string }) {
  const [incident, setIncident] = useState<IncidentDTO | null>(null);
  const [events, setEvents] = useState<IncidentEventDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [retries, setRetries] = useState(0);

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);
    setIncident(null);
    setEvents([]);

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

  if (problem) return <ErrorState problem={problem} onRetry={() => setRetries((n) => n + 1)} />;

  if (loading && !incident) {
    return (
      <Box color="text-body-secondary" padding="l">
        Loading incident…
      </Box>
    );
  }

  if (!incident) return null;

  return (
    <SpaceBetween size="l">
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
    </SpaceBetween>
  );
}
