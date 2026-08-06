import { useCallback, useEffect, useRef, useState } from 'react';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import Grid from '@cloudscape-design/components/grid';
import type { GridProps } from '@cloudscape-design/components/grid';
import Header from '@cloudscape-design/components/header';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import { getNOCOverview } from '../noc';
import type { NOCOverview } from '../noc';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { ErrorState } from '../components/States';

/** Refresh cadence. Matches ADR-NOC-002's own polling interval — v1 is polling, not streaming (D2 stays open, see ADR-NOC-003). */
const POLL_INTERVAL_MS = 15_000;

/** One grid cell per widget: 1 column on phones, 2 on tablets, 3 from "medium" up. */
const WIDGET_COLSPAN: GridProps.ElementDefinition = { colspan: { default: 12, xs: 6, m: 4 } };

/**
 * A count rendered through Cloudscape's semantic `StatusIndicator`, never an
 * ad-hoc color: zero is always `success` ("healthy"), a non-zero count takes
 * the severity-appropriate type the caller supplies. This is the same fixed,
 * small palette ADR-NOC-002 established for the vanilla page it replaces
 * (red=critical/high/open, amber=warning/medium, blue=info/low, green=zeroed)
 * — carried forward so the reading doesn't change, only the renderer.
 */
function Count({ count, type }: { count: number; type: StatusIndicatorProps.Type }) {
  return <StatusIndicator type={count === 0 ? 'success' : type}>{count}</StatusIndicator>;
}

function formatGeneratedAt(iso: string): string {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
}

function IncidentsWidget({ data }: { data: NOCOverview['incidents'] }) {
  return (
    <Container
      header={
        <Header variant="h2" description="Open-class incidents: open, acknowledged, investigating.">
          Incidents
        </Header>
      }
    >
      <SpaceBetween size="m">
        <KeyValuePairs
          columns={1}
          items={[{ label: 'Open total', value: <Count count={data.open_total} type="error" /> }]}
        />
        <div>
          <Box variant="awsui-key-label">By status</Box>
          <KeyValuePairs
            columns={3}
            items={[
              { label: 'Open', value: <Count count={data.by_status.open} type="error" /> },
              { label: 'Acknowledged', value: <Count count={data.by_status.acknowledged} type="warning" /> },
              { label: 'Investigating', value: <Count count={data.by_status.investigating} type="info" /> },
            ]}
          />
        </div>
        <div>
          <Box variant="awsui-key-label">By severity</Box>
          <KeyValuePairs
            columns={4}
            items={[
              { label: 'Critical', value: <Count count={data.by_severity.critical} type="error" /> },
              { label: 'High', value: <Count count={data.by_severity.high} type="error" /> },
              { label: 'Medium', value: <Count count={data.by_severity.medium} type="warning" /> },
              { label: 'Low', value: <Count count={data.by_severity.low} type="info" /> },
            ]}
          />
        </div>
        <div>
          <Box variant="awsui-key-label">Grouping</Box>
          <KeyValuePairs
            columns={2}
            items={[
              { label: 'Root incidents', value: <Count count={data.grouped.root_count} type="info" /> },
              { label: 'Collateral', value: <Count count={data.grouped.collateral_count} type="info" /> },
            ]}
          />
        </div>
      </SpaceBetween>
    </Container>
  );
}

function AlertsWidget({ data }: { data: NOCOverview['alerts'] }) {
  return (
    <Container header={<Header variant="h2" description="Currently firing alert rules.">Alerts firing</Header>}>
      <SpaceBetween size="m">
        <KeyValuePairs
          columns={1}
          items={[{ label: 'Firing total', value: <Count count={data.firing_total} type="error" /> }]}
        />
        <div>
          <Box variant="awsui-key-label">By severity</Box>
          <KeyValuePairs
            columns={3}
            items={[
              { label: 'Critical', value: <Count count={data.by_severity.critical} type="error" /> },
              { label: 'Warning', value: <Count count={data.by_severity.warning} type="warning" /> },
              { label: 'Info', value: <Count count={data.by_severity.info} type="info" /> },
            ]}
          />
        </div>
      </SpaceBetween>
    </Container>
  );
}

function AssetsWidget({ data }: { data: NOCOverview['assets'] }) {
  const allClear =
    data.stale === 0 &&
    data.orphaned_assets === 0 &&
    data.orphaned_business_services === 0 &&
    data.incomplete === 0;

  return (
    <Container header={<Header variant="h2" description="Asset health categories (E1.5).">Asset health</Header>}>
      {allClear ? (
        <StatusIndicator type="success">All clear</StatusIndicator>
      ) : (
        <KeyValuePairs
          columns={2}
          items={[
            { label: 'Stale', value: <Count count={data.stale} type="warning" /> },
            { label: 'Orphaned assets', value: <Count count={data.orphaned_assets} type="warning" /> },
            {
              label: 'Orphaned business services',
              value: <Count count={data.orphaned_business_services} type="warning" />,
            },
            { label: 'Incomplete', value: <Count count={data.incomplete} type="warning" /> },
          ]}
        />
      )}
    </Container>
  );
}

function OnCallWidget({ data }: { data: NOCOverview['on_call'] }) {
  return (
    <Container header={<Header variant="h2" description="Who is on call right now, per active schedule.">On call now</Header>}>
      {data.length === 0 ? (
        <Box color="text-body-secondary">No active on-call schedules.</Box>
      ) : (
        <KeyValuePairs
          columns={2}
          items={data.map((e) => ({
            label: e.schedule_name,
            value: e.display_name ?? <Box color="text-body-secondary">Unassigned</Box>,
          }))}
        />
      )}
    </Container>
  );
}

function EscalationsWidget({ data }: { data: NOCOverview['escalations'] }) {
  return (
    <Container header={<Header variant="h2" description="Escalations currently active.">Escalations</Header>}>
      <KeyValuePairs
        columns={1}
        items={[{ label: 'Active total', value: <Count count={data.active_total} type="warning" /> }]}
      />
    </Container>
  );
}

/**
 * The NOC command-center overview: the whole operational loop's current state
 * in one screen, polled from `GET /v1/admin/noc/overview` (E7.1, ADR-NOC-001).
 * Replaces the vanilla `/noc` stopgap (ADR-NOC-002, retired by ADR-NOC-003)
 * inside the shared console shell.
 */
export function NOCOverviewPage() {
  const [overview, setOverview] = useState<NOCOverview | null>(null);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const timerRef = useRef<number>();
  const abortRef = useRef<AbortController>();
  const runCycleRef = useRef<() => void>();

  const load = useCallback(() => {
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    setLoading(true);
    getNOCOverview(ctrl.signal)
      .then((data) => {
        setOverview(data);
        setProblem(null);
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not reach OneOps', status: 0, detail: String(err) },
        );
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });
  }, []);

  // Poll on a fixed cadence, scheduled only after each fetch settles — a slow
  // response cannot pile up overlapping in-flight requests. Manual refresh
  // reuses the same cycle so it never runs concurrently with the timer.
  useEffect(() => {
    const runCycle = () => {
      load();
      timerRef.current = window.setTimeout(runCycle, POLL_INTERVAL_MS);
    };
    runCycleRef.current = runCycle;
    runCycle();
    return () => {
      window.clearTimeout(timerRef.current);
      abortRef.current?.abort();
    };
  }, [load]);

  const refresh = useCallback(() => {
    window.clearTimeout(timerRef.current);
    runCycleRef.current?.();
  }, []);

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Live operational state of the NOC loop: incidents, alerts, asset health, on-call and escalations."
        actions={
          <SpaceBetween direction="horizontal" size="s" alignItems="center">
            {overview && (
              <Box color="text-body-secondary" fontSize="body-s">
                Last updated {formatGeneratedAt(overview.generated_at)}
              </Box>
            )}
            <Button iconName="refresh" onClick={refresh} loading={loading} loadingText="Refreshing">
              Refresh
            </Button>
          </SpaceBetween>
        }
      >
        NOC / Overview
      </Header>

      {problem && <ErrorState problem={problem} onRetry={refresh} />}

      {!overview && !problem && (
        <Box margin="xxl" textAlign="center" padding="xxl">
          <div role="status" aria-busy="true">
            <Spinner size="large" /> Loading operational state…
          </div>
        </Box>
      )}

      {overview && (
        <Grid
          gridDefinition={[WIDGET_COLSPAN, WIDGET_COLSPAN, WIDGET_COLSPAN, WIDGET_COLSPAN, WIDGET_COLSPAN]}
        >
          <IncidentsWidget data={overview.incidents} />
          <AlertsWidget data={overview.alerts} />
          <AssetsWidget data={overview.assets} />
          <OnCallWidget data={overview.on_call} />
          <EscalationsWidget data={overview.escalations} />
        </Grid>
      )}
    </SpaceBetween>
  );
}
