import { getToken } from './auth';

// Typed contract for the OneOps Configuration Registry API.
// Mirrors internal/httpapi/openapi.yaml (ConfigObject) and internal/domain/enums.go.
// Kept hand-written and small; generate from the spec when a second consumer appears.

export const ROLES = [
  'constitution',
  'governance',
  'eng_spec',
  'validation',
  'evidence',
  'audit',
  'planning',
  'reference',
  'working',
] as const;

export const LIFECYCLES = [
  'draft',
  'in_review',
  'ratified',
  'approved',
  'in_progress',
  'complete',
  'suspended',
  'deprecated',
  'withdrawn',
] as const;

export const AUTHORITIES = ['active', 'historical', 'non_normative'] as const;

export type Role = (typeof ROLES)[number];
export type Lifecycle = (typeof LIFECYCLES)[number];
export type Authority = (typeof AUTHORITIES)[number];

export type RetentionClass =
  | 'current_baseline'
  | 'current_planning'
  | 'historical_record'
  | 'superseded_planning'
  | 'historical_evidence'
  | 'audit_record'
  | 'working_material';

export interface ConfigObject {
  cfg_id: string;
  artifact: string;
  version: string;
  role: Role;
  lifecycle: Lifecycle;
  retention_class: RetentionClass;
  authority: Authority;
  ratified_by?: string;
  review_cycle?: string;
  retention_policy?: string;
  metadata?: Record<string, string>;
  row_version: number;
  created_at: string;
  updated_at: string;
}

export interface Page {
  items: ConfigObject[];
  next_cursor?: string;
}

export interface EstateFilter {
  role?: Role | '';
  lifecycle?: Lifecycle | '';
  authority?: Authority | '';
  q?: string;
  cursor?: string;
  limit?: number;
}

/** RFC 7807 problem detail — the API's error contract on every failure path. */
export interface ProblemDetail {
  type?: string;
  title: string;
  status: number;
  detail?: string;
  instance?: string;
}

export class ApiError extends Error {
  readonly problem: ProblemDetail;
  constructor(problem: ProblemDetail) {
    super(problem.detail || problem.title);
    this.problem = problem;
  }
}

/** Graph traversal result. Nodes are graph primitives — no authority or lifecycle. */
export interface TraversalNode {
  cfg_id: string;
  depth: number;
}

export interface TraversalResponse {
  root: string;
  direction: 'dependencies' | 'dependents';
  recursive: boolean;
  count: number;
  nodes: TraversalNode[];
}

/** A related artifact with its name resolved, when resolution succeeded. */
export interface Relation {
  cfg_id: string;
  artifact?: string;
  authority?: Authority;
}

function toQuery(f: EstateFilter): string {
  const p = new URLSearchParams();
  if (f.role) p.set('role', f.role);
  if (f.lifecycle) p.set('lifecycle', f.lifecycle);
  if (f.authority) p.set('authority', f.authority);
  if (f.q) p.set('q', f.q);
  if (f.cursor) p.set('cursor', f.cursor);
  p.set('limit', String(f.limit ?? 50));
  return p.toString();
}

export async function getJSON<T>(url: string, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  const token = getToken();
  if (token) headers.Authorization = `Bearer ${token}`;

  const res = await fetch(url, { headers, signal });

  if (!res.ok) {
    let problem: ProblemDetail = {
      title: 'Request failed',
      status: res.status,
      detail: `The server returned ${res.status}.`,
    };
    try {
      problem = { ...problem, ...(await res.json()) };
    } catch {
      // Non-JSON error body; keep the fallback above.
    }
    throw new ApiError(problem);
  }

  return (await res.json()) as T;
}

/**
 * POSTs a JSON body and decodes a JSON response, throwing `ApiError` on any
 * non-2xx status (ADR-ACT-001's write-action contract). No `Idempotency-Key`:
 * unlike `/v1/governance/*` (`executeGovernance`), none of the endpoints this
 * helper calls check for one (verified against `handlers_incidents.go` — only
 * `handlers_configobject.go`/`handlers_governance.go` read that header), so
 * adding one here would be a header nobody reads rather than a real retry-
 * safety mechanism.
 */
export async function postJSON<T>(url: string, body: unknown, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json', 'Content-Type': 'application/json' };
  const token = getToken();
  if (token) headers.Authorization = `Bearer ${token}`;

  const res = await fetch(url, { method: 'POST', headers, body: JSON.stringify(body), signal });

  if (!res.ok) {
    let problem: ProblemDetail = {
      title: 'Request failed',
      status: res.status,
      detail: `The server returned ${res.status}.`,
    };
    try {
      problem = { ...problem, ...(await res.json()) };
    } catch {
      // Non-JSON error body; keep the fallback above.
    }
    throw new ApiError(problem);
  }

  return (await res.json()) as T;
}

/** Result of a §8 governance operation. `state` is absent when the object was removed. */
export interface GovernanceResult {
  operation: string;
  cfg_id: string;
  actor: string;
  occurred_at: string;
  row_version: number;
  removed?: boolean;
  state?: {
    lifecycle: Lifecycle;
    retention_class: RetentionClass;
    authority: Authority;
  };
  audit: { operation_id: string; chain_id: string; recorded: boolean };
}

/**
 * Executes a §8 governance operation.
 *
 * Verified against the handler: `Idempotency-Key` is **required** (400 without it)
 * and `If-Match` is optional optimistic concurrency. The actor is taken from the
 * bearer token server-side — it is never sent by the client.
 *
 * `idempotencyKey` is supplied by the caller so a retry of the *same* user intent
 * reuses it; a fresh intent must pass a fresh key.
 */
export async function executeGovernance(
  cfgId: string,
  operation: 'ratify' | 'approve' | 'suspend' | 'deprecate' | 'withdraw',
  opts: { rowVersion?: number; idempotencyKey: string },
): Promise<GovernanceResult> {
  const headers: Record<string, string> = {
    Accept: 'application/json',
    'Idempotency-Key': opts.idempotencyKey,
  };
  const token = getToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  if (opts.rowVersion !== undefined) headers['If-Match'] = `"${opts.rowVersion}"`;

  const res = await fetch(
    `/v1/governance/${encodeURIComponent(cfgId)}/${operation}`,
    { method: 'POST', headers },
  );

  if (!res.ok) {
    let problem: ProblemDetail = {
      title: 'Operation failed',
      status: res.status,
      detail: `The server returned ${res.status}.`,
    };
    try {
      problem = { ...problem, ...(await res.json()) };
    } catch {
      // Non-JSON error body; keep the fallback above.
    }
    throw new ApiError(problem);
  }

  return (await res.json()) as GovernanceResult;
}

/** A fresh idempotency key represents one new user intent. */
export const newIdempotencyKey = () =>
  typeof crypto.randomUUID === 'function'
    ? crypto.randomUUID()
    : `k-${Date.now()}-${Math.random().toString(36).slice(2)}`;

/** Lists configuration objects. Throws ApiError carrying the server's problem+json. */
export function listArtifacts(f: EstateFilter, signal?: AbortSignal): Promise<Page> {
  return getJSON<Page>(`/v1/artifacts?${toQuery(f)}`, signal);
}

export function getArtifact(id: string, signal?: AbortSignal): Promise<ConfigObject> {
  return getJSON<ConfigObject>(`/v1/artifacts/${encodeURIComponent(id)}`, signal);
}

function getTraversal(
  id: string,
  direction: 'dependencies' | 'dependents',
  signal?: AbortSignal,
): Promise<TraversalResponse> {
  return getJSON<TraversalResponse>(
    `/v1/configurations/${encodeURIComponent(id)}/${direction}`,
    signal,
  );
}

/** Cap on name resolution. Direct relations are normally few; this bounds the pathological case. */
const RESOLVE_LIMIT = 25;

/**
 * Direct relations with names resolved where possible.
 *
 * The graph endpoints return `{cfg_id, depth}` only — by design, they are graph
 * primitives carrying no authority or lifecycle. A human needs the artifact name,
 * so each direct relation is fetched once, in parallel, up to RESOLVE_LIMIT.
 * Resolution failure degrades to the bare id rather than failing the view.
 */
export async function getRelations(
  id: string,
  direction: 'dependencies' | 'dependents',
  signal?: AbortSignal,
): Promise<{ total: number; relations: Relation[] }> {
  const traversal = await getTraversal(id, direction, signal);
  const nodes = traversal.nodes ?? [];

  const resolved = await Promise.all(
    nodes.slice(0, RESOLVE_LIMIT).map(async (n): Promise<Relation> => {
      try {
        const o = await getArtifact(n.cfg_id, signal);
        return { cfg_id: n.cfg_id, artifact: o.artifact, authority: o.authority };
      } catch {
        return { cfg_id: n.cfg_id };
      }
    }),
  );

  return { total: traversal.count ?? nodes.length, relations: resolved };
}
