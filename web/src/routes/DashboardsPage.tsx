import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import Grid from '@cloudscape-design/components/grid';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import LineChart from '@cloudscape-design/components/line-chart';
import BarChart from '@cloudscape-design/components/bar-chart';
import MixedLineBarChart from '@cloudscape-design/components/mixed-line-bar-chart';
import type { MixedLineBarChartProps } from '@cloudscape-design/components/mixed-line-bar-chart';
import Select from '@cloudscape-design/components/select';
import type { SelectProps } from '@cloudscape-design/components/select';
import SegmentedControl from '@cloudscape-design/components/segmented-control';
import type { SegmentedControlProps } from '@cloudscape-design/components/segmented-control';
import SpaceBetween from '@cloudscape-design/components/space-between';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { getAssetGraph } from '../assetGraph';
import type { AssetGraphNode } from '../assetGraph';
import { getIncidentTrends } from '../dashboards';
import type { IncidentTrendBucket, IncidentTrendPoint } from '../dashboards';
import { humanise, RESOLVED_CHART_COLOR, SEVERITY_CHART_COLOR } from '../incidentPresentation';
import { ErrorState } from '../components/States';
import { queryTelemetry } from '../telemetry';
import type { TelemetrySample } from '../telemetry';

/** Matches NOCOverviewPage's own polling cadence (ADR-NOC-002/003) — v1 is polling, not streaming (D2 stays open). */
const POLL_INTERVAL_MS = 15_000;

type RangeId = '24h' | '7d' | '30d';

const RANGE_OPTIONS: SegmentedControlProps.Option[] = [
  { id: '24h', text: 'Last 24h' },
  { id: '7d', text: 'Last 7d' },
  { id: '30d', text: 'Last 30d' },
];

interface RangeWindow {
  from: Date;
  to: Date;
  bucket: IncidentTrendBucket;
}

/**
 * The 24h view buckets by hour (24 buckets), the 7d/30d views bucket by day
 * (7/30 buckets) — all far under domain.MaxIncidentTrendBuckets (744, a month
 * of hourly buckets), so none of these three options can ever be refused by
 * the server's own cap.
 */
function windowFor(rangeId: RangeId, now: Date): RangeWindow {
  const days = rangeId === '24h' ? 1 : rangeId === '7d' ? 7 : 30;
  const bucket: IncidentTrendBucket = rangeId === '24h' ? 'hour' : 'day';
  return { from: new Date(now.getTime() - days * 24 * 3_600_000), to: now, bucket };
}

function bucketLabel(iso: string, bucket: IncidentTrendBucket): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return bucket === 'hour'
    ? d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit' })
    : d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

const SEVERITIES = ['critical', 'high', 'medium', 'low'] as const;

/**
 * The primary chart: incidents opened per bucket, stacked by severity
 * (`SEVERITY_CHART_COLOR`, the same palette `SEVERITY_TYPE` establishes for
 * every `StatusIndicator` elsewhere), plus a resolved-incidents line. `x` is
 * a formatted bucket label (categorical — Cloudscape's own guidance for bar
 * charts), never the raw ISO timestamp.
 */
function IncidentVolumeChart({
  points,
  bucket,
  loading,
}: {
  points: IncidentTrendPoint[];
  bucket: IncidentTrendBucket;
  loading: boolean;
}) {
  const series = useMemo<MixedLineBarChartProps.ChartSeries<string>[]>(() => {
    const labels = points.map((p) => bucketLabel(p.bucket_start, bucket));
    const bars: MixedLineBarChartProps.BarDataSeries<string>[] = SEVERITIES.map((sev) => ({
      title: humanise(sev),
      type: 'bar',
      color: SEVERITY_CHART_COLOR[sev],
      data: points.map((p, i) => ({ x: labels[i], y: p.opened_by_severity[sev] })),
    }));
    const resolved: MixedLineBarChartProps.LineDataSeries<string> = {
      title: 'Resolved',
      type: 'line',
      color: RESOLVED_CHART_COLOR,
      data: points.map((p, i) => ({ x: labels[i], y: p.resolved_total })),
    };
    return [...bars, resolved];
  }, [points, bucket]);

  return (
    <MixedLineBarChart<string>
      series={series}
      xScaleType="categorical"
      stackedBars
      height={280}
      xTitle="Time"
      yTitle="Incidents"
      ariaLabel="Incidents opened per bucket, by severity, with resolved incidents overlaid"
      statusType={loading && points.length === 0 ? 'loading' : 'finished'}
      loadingText="Loading incident trends"
      empty={
        <Box textAlign="center" color="inherit">
          No incident data for this window.
        </Box>
      }
      detailPopoverSize="medium"
    />
  );
}

/**
 * The alert-volume proxy chart (E9.1/ADR-NOC-008 §5, optional per the E9.2
 * brief): `opened_by_source` split manual/alert. Labeled honestly — this is
 * NOT a firing log, only how many incidents the correlation pipeline
 * created or linked off a firing.
 */
function AlertSourceChart({ points, bucket, loading }: { points: IncidentTrendPoint[]; bucket: IncidentTrendBucket; loading: boolean }) {
  const series = useMemo<MixedLineBarChartProps.BarDataSeries<string>[]>(() => {
    const labels = points.map((p) => bucketLabel(p.bucket_start, bucket));
    return [
      {
        title: 'Manual',
        type: 'bar',
        data: points.map((p, i) => ({ x: labels[i], y: p.opened_by_source.manual })),
      },
      {
        title: 'Alert-sourced (proxy)',
        type: 'bar',
        color: '#7d8998',
        data: points.map((p, i) => ({ x: labels[i], y: p.opened_by_source.alert })),
      },
    ];
  }, [points, bucket]);

  return (
    <BarChart<string>
      series={series}
      xScaleType="categorical"
      stackedBars
      height={220}
      xTitle="Time"
      yTitle="Incidents"
      ariaLabel="Incidents opened per bucket, by source"
      statusType={loading && points.length === 0 ? 'loading' : 'finished'}
      loadingText="Loading source breakdown"
      empty={
        <Box textAlign="center" color="inherit">
          No incident data for this window.
        </Box>
      }
    />
  );
}

/**
 * The secondary chart: one asset+metric's rollup_5m telemetry over the last
 * 24h. Renders Cloudscape's own `empty` state when the dev database has no
 * samples yet — never a crash on an empty `items` array.
 */
function TelemetryChart({ items, metric, loading }: { items: TelemetrySample[]; metric: string; loading: boolean }) {
  const series = useMemo<MixedLineBarChartProps.LineDataSeries<Date>[]>(() => {
    if (items.length === 0) return [];
    return [
      {
        title: metric || 'value',
        type: 'line',
        data: items.map((s) => ({ x: new Date(s.timestamp), y: s.value })),
      },
    ];
  }, [items, metric]);

  return (
    <LineChart<Date>
      series={series}
      xScaleType="time"
      height={220}
      xTitle="Time"
      yTitle={metric || 'Value'}
      ariaLabel="Telemetry value over time"
      statusType={loading ? 'loading' : 'finished'}
      loadingText="Loading telemetry"
      empty={
        <Box textAlign="center" color="inherit">
          <SpaceBetween size="xs">
            <b>No telemetry samples</b>
            <Box variant="p" color="text-body-secondary">
              No rollup_5m data for this asset and metric in the last 24h.
            </Box>
          </SpaceBetween>
        </Box>
      }
    />
  );
}

/** Metric example straight out of domain.Sample's own validation error message — a real name, not an invented one. */
const DEFAULT_METRIC = 'cpu_utilization';

/**
 * The Dashboards screen: trend charts over time, completing E9/visibility.
 * Incident volume (E9.1, ADR-NOC-008) is the centerpiece; a telemetry-trend
 * chart over an existing asset+metric (E2.1b) is secondary. Cloudscape
 * NATIVE charts only — `LineChart`/`BarChart`/`MixedLineBarChart`, already
 * bundled, no new dependency (ADR-NOC-009).
 */
export function DashboardsPage() {
  const [rangeId, setRangeId] = useState<RangeId>('24h');
  const [points, setPoints] = useState<IncidentTrendPoint[]>([]);
  const [bucket, setBucket] = useState<IncidentTrendBucket>('hour');
  const [trendsLoading, setTrendsLoading] = useState(true);
  const [trendsProblem, setTrendsProblem] = useState<ProblemDetail | null>(null);
  const trendsTimerRef = useRef<number>();
  const trendsAbortRef = useRef<AbortController>();
  const trendsRunCycleRef = useRef<() => void>();

  const loadTrends = useCallback(() => {
    trendsAbortRef.current?.abort();
    const ctrl = new AbortController();
    trendsAbortRef.current = ctrl;
    setTrendsLoading(true);
    const { from, to, bucket: b } = windowFor(rangeId, new Date());
    getIncidentTrends(from, to, b, ctrl.signal)
      .then((resp) => {
        setPoints(resp.points);
        setBucket(resp.bucket);
        setTrendsProblem(null);
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setTrendsProblem(
          err instanceof ApiError ? err.problem : { title: 'Could not load incident trends', status: 0, detail: String(err) },
        );
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setTrendsLoading(false);
      });
  }, [rangeId]);

  useEffect(() => {
    const runCycle = () => {
      loadTrends();
      trendsTimerRef.current = window.setTimeout(runCycle, POLL_INTERVAL_MS);
    };
    trendsRunCycleRef.current = runCycle;
    runCycle();
    return () => {
      window.clearTimeout(trendsTimerRef.current);
      trendsAbortRef.current?.abort();
    };
  }, [loadTrends]);

  // --- Telemetry picker + chart -------------------------------------------------

  const [assets, setAssets] = useState<AssetGraphNode[]>([]);
  const [assetsProblem, setAssetsProblem] = useState<ProblemDetail | null>(null);
  const [selectedAssetId, setSelectedAssetId] = useState('');
  const [metricDraft, setMetricDraft] = useState(DEFAULT_METRIC);
  const [metric, setMetric] = useState(DEFAULT_METRIC);

  useEffect(() => {
    const ctrl = new AbortController();
    getAssetGraph(ctrl.signal)
      .then((graph) => {
        const nodes = [...graph.nodes].sort((a, b) => a.name.localeCompare(b.name));
        setAssets(nodes);
        setSelectedAssetId((prev) => prev || nodes[0]?.asset_id || '');
        setAssetsProblem(null);
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setAssetsProblem(
          err instanceof ApiError ? err.problem : { title: 'Could not load assets', status: 0, detail: String(err) },
        );
      });
    return () => ctrl.abort();
  }, []);

  const [telemetryItems, setTelemetryItems] = useState<TelemetrySample[]>([]);
  const [telemetryLoading, setTelemetryLoading] = useState(false);
  const [telemetryProblem, setTelemetryProblem] = useState<ProblemDetail | null>(null);
  const telemetryTimerRef = useRef<number>();
  const telemetryAbortRef = useRef<AbortController>();
  const telemetryRunCycleRef = useRef<() => void>();

  const loadTelemetry = useCallback(() => {
    if (!selectedAssetId || !metric) {
      setTelemetryItems([]);
      setTelemetryProblem(null);
      return;
    }
    telemetryAbortRef.current?.abort();
    const ctrl = new AbortController();
    telemetryAbortRef.current = ctrl;
    setTelemetryLoading(true);
    const to = new Date();
    const from = new Date(to.getTime() - 24 * 3_600_000);
    queryTelemetry(selectedAssetId, metric, from, to, 'rollup_5m', ctrl.signal)
      .then((resp) => {
        setTelemetryItems(resp.items);
        setTelemetryProblem(null);
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setTelemetryItems([]);
        setTelemetryProblem(
          err instanceof ApiError ? err.problem : { title: 'Could not load telemetry', status: 0, detail: String(err) },
        );
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setTelemetryLoading(false);
      });
  }, [selectedAssetId, metric]);

  useEffect(() => {
    const runCycle = () => {
      loadTelemetry();
      telemetryTimerRef.current = window.setTimeout(runCycle, POLL_INTERVAL_MS);
    };
    telemetryRunCycleRef.current = runCycle;
    runCycle();
    return () => {
      window.clearTimeout(telemetryTimerRef.current);
      telemetryAbortRef.current?.abort();
    };
  }, [loadTelemetry]);

  const refresh = useCallback(() => {
    window.clearTimeout(trendsTimerRef.current);
    trendsRunCycleRef.current?.();
    window.clearTimeout(telemetryTimerRef.current);
    telemetryRunCycleRef.current?.();
  }, []);

  const assetOptions: SelectProps.Option[] = assets.map((a) => ({
    value: a.asset_id,
    label: `${a.name} (${a.asset_id})`,
  }));
  const selectedAssetOption = assetOptions.find((o) => o.value === selectedAssetId) ?? null;

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Trend charts over time: incident volume (E9.1) and one asset's telemetry."
        actions={
          <Button iconName="refresh" onClick={refresh} loading={trendsLoading || telemetryLoading} loadingText="Refreshing">
            Refresh
          </Button>
        }
      >
        Dashboards
      </Header>

      {trendsProblem && <ErrorState problem={trendsProblem} onRetry={refresh} />}

      {!trendsProblem && (
        <Grid gridDefinition={[{ colspan: 12 }, { colspan: { default: 12, m: 6 } }, { colspan: { default: 12, m: 6 } }]}>
          <Container
            header={
              <Header
                variant="h2"
                description="Incidents opened per bucket, by severity, with resolved incidents overlaid."
                actions={
                  <SegmentedControl
                    selectedId={rangeId}
                    onChange={({ detail }) => setRangeId(detail.selectedId as RangeId)}
                    options={RANGE_OPTIONS}
                    label="Time range"
                  />
                }
              >
                Incident volume
              </Header>
            }
          >
            <IncidentVolumeChart points={points} bucket={bucket} loading={trendsLoading} />
          </Container>

          <Container
            header={
              <Header variant="h2" description="An incident-source proxy — NOT a firing log (ADR-NOC-008 §5).">
                Alert-sourced incidents
              </Header>
            }
          >
            <AlertSourceChart points={points} bucket={bucket} loading={trendsLoading} />
          </Container>

          <Container
            header={
              <Header
                variant="h2"
                description="rollup_5m telemetry for one asset and metric, last 24h."
                actions={
                  <SpaceBetween direction="horizontal" size="xs" alignItems="center">
                    <Select
                      selectedOption={selectedAssetOption}
                      onChange={({ detail }) => setSelectedAssetId(detail.selectedOption.value ?? '')}
                      options={assetOptions}
                      placeholder="Select an asset"
                      empty="No assets in this tenant"
                      ariaLabel="Asset"
                      disabled={assets.length === 0}
                    />
                    <Input
                      value={metricDraft}
                      onChange={({ detail }) => setMetricDraft(detail.value)}
                      onKeyDown={({ detail }) => {
                        if (detail.key === 'Enter') setMetric(metricDraft);
                      }}
                      placeholder="metric, e.g. cpu_utilization"
                      ariaLabel="Metric"
                    />
                    <Button onClick={() => setMetric(metricDraft)}>Load</Button>
                  </SpaceBetween>
                }
              >
                Telemetry trend
              </Header>
            }
          >
            {assetsProblem && <ErrorState problem={assetsProblem} onRetry={refresh} />}
            {telemetryProblem && <ErrorState problem={telemetryProblem} onRetry={refresh} />}
            {!telemetryProblem && <TelemetryChart items={telemetryItems} metric={metric} loading={telemetryLoading} />}
          </Container>
        </Grid>
      )}
    </SpaceBetween>
  );
}
