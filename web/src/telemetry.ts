import { getJSON } from './api';

// Typed contract for GET /v1/admin/telemetry. Mirrors
// internal/httpapi/handlers_telemetry.go's sampleDTO exactly — field names
// and shape, not a paraphrase. Kept hand-written, like noc.ts/dashboards.ts,
// until a second consumer of the generated spec appears.

/** Only `rollup_5m` is used by the dashboards screen — raw/auto are the ingest-adjacent, higher-volume shapes this chart deliberately does not ask for. */
export type TelemetryResolution = 'raw' | 'rollup_5m' | 'auto';

/** Min/Max/Count are present only at ResolutionRollup5m — see domain.Sample's doc comment. */
export interface TelemetrySample {
  asset_id: string;
  metric: string;
  value: number;
  timestamp: string;
  labels: Record<string, string>;
  min?: number;
  max?: number;
  count?: number;
}

export function queryTelemetry(
  assetId: string,
  metric: string,
  from: Date,
  to: Date,
  resolution: TelemetryResolution,
  signal?: AbortSignal,
): Promise<{ items: TelemetrySample[] }> {
  const p = new URLSearchParams({
    asset_id: assetId,
    metric,
    from: from.toISOString(),
    to: to.toISOString(),
    resolution,
  });
  return getJSON<{ items: TelemetrySample[] }>(`/v1/admin/telemetry?${p.toString()}`, signal);
}
