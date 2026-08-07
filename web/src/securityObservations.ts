import { getJSON } from './api';
import type { ObservationSeverity } from './securityRules';

// Typed contract for GET /v1/admin/security-observations (E-SEC-UI.4).
// Mirrors internal/httpapi/handlers_security_observations.go's
// securityObservationDTO (lines 127-134) and openapi.yaml's SecurityObservation
// schema (lines 4885-4898) field-for-field. READ-ONLY — this file
// deliberately carries no create/edit/delete function: security_observation
// is append-only telemetry (POST /admin/security-observations is an ingest
// endpoint for producers, not a console write action; E-SEC-UI.4's brief is
// explicit that this screen has no mutate UI). Kept hand-written, like
// telemetry.ts, until a second consumer of the generated spec appears.

/**
 * domain.ObservationSeverity's closed vocabulary — the SAME scale
 * security_rule's min_severity already carries (securityRules.ts'
 * OBSERVATION_SEVERITIES), re-exported here rather than redefined so the two
 * never drift apart.
 */
export { OBSERVATION_SEVERITIES } from './securityRules';
export type { ObservationSeverity } from './securityRules';

export interface SecurityObservationDTO {
  asset_id: string;
  observation_type: string;
  source: string;
  severity: ObservationSeverity;
  observed_at: string;
  attributes: Record<string, string>;
}

/**
 * CONFIRMED CONTRACT FACT: `querySecurityObservations`
 * (handlers_security_observations.go:151-212, openapi.yaml:1949-1988) is a
 * bounded RANGE query over ONE asset's ONE observation type — `asset_id`,
 * `observation_type`, `from` and `to` are ALL required query parameters; the
 * endpoint has no "list everything" mode and no severity filter of its own
 * (severity is filtered client-side over the fetched page, the same
 * after-the-fact narrowing DetectionRulesPage/IndicatorsPage already apply
 * to their own client-only filters). `after` is a keyset cursor (the
 * `observed_at` of the previous page's last item); `limit` is clamped
 * server-side to `domain.MaxSecurityObservationQueryLimit` (5000).
 */
export interface QuerySecurityObservationsOptions {
  limit?: number;
  after?: Date;
}

/** Mirrors domain.DefaultSecurityObservationQueryLimit (internal/domain/security_observation.go:203) — this console's own default page size when the operator has not asked for more. */
export const SECURITY_OBSERVATION_DEFAULT_LIMIT = 500;
/** Mirrors domain.MaxSecurityObservationQueryLimit (internal/domain/security_observation.go:204) — the server's own hard cap; a value above this here would just be clamped, so the picker is bounded to it too. */
export const SECURITY_OBSERVATION_MAX_LIMIT = 5000;

export function querySecurityObservations(
  assetId: string,
  observationType: string,
  from: Date,
  to: Date,
  opts: QuerySecurityObservationsOptions = {},
  signal?: AbortSignal,
): Promise<{ items: SecurityObservationDTO[] }> {
  const p = new URLSearchParams({
    asset_id: assetId,
    observation_type: observationType,
    from: from.toISOString(),
    to: to.toISOString(),
    limit: String(opts.limit ?? SECURITY_OBSERVATION_DEFAULT_LIMIT),
  });
  if (opts.after) p.set('after', opts.after.toISOString());
  return getJSON<{ items: SecurityObservationDTO[] }>(`/v1/admin/security-observations?${p.toString()}`, signal);
}
