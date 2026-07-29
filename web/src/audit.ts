import { getJSON } from './api';
import type { Authority, Lifecycle, RetentionClass } from './api';

/**
 * The evidence view is assembled from two endpoints, joined on `seq`:
 *
 *   /v1/governance/{id}/history      — what the operation produced (resulting_state)
 *   /v1/governance/{id}/audit/events — the tamper-evident hashes (prev_hash/this_hash)
 *
 * Neither is sufficient alone: history carries no hashes, and audit/events carries
 * no state. Verified against handlers_governance_read.go, not assumed.
 */

export interface ResultingState {
  lifecycle: Lifecycle;
  retention_class: RetentionClass;
  authority: Authority;
}

interface HistoryItem {
  seq: number;
  operation: string;
  actor: string;
  occurred_at: string;
  operation_id: string;
  removed?: boolean;
  resulting_state?: ResultingState;
}

interface HistoryResponse {
  items: HistoryItem[];
  next_cursor?: string;
}

interface AuditEventItem {
  seq: number;
  event_id: string;
  operation_id: string;
  operation: string;
  actor: string;
  occurred_at: string;
  prev_hash?: string;
  this_hash: string;
}

interface AuditEventsResponse {
  items: AuditEventItem[];
  order: 'asc' | 'desc';
  next_cursor?: string;
}

export interface ChainSummary {
  chain_id: string;
  verified: boolean;
  checked: number;
  head_seq: number;
  head_hash?: string;
  first_break_seq?: number;
  break_reason?: string;
}

/** One timeline entry: what happened, what it produced, and its place in the chain. */
export interface AuditEntry {
  seq: number;
  operation: string;
  actor: string;
  occurredAt: string;
  operationId: string;
  removed: boolean;
  state?: ResultingState;
  /** The state produced by the preceding operation, when there is one. */
  previousState?: ResultingState;
  eventId?: string;
  prevHash?: string;
  thisHash?: string;
}

export interface Timeline {
  /** Newest first — the order a reader wants. */
  entries: AuditEntry[];
  nextCursor?: string;
}

/**
 * Loads a page of governance evidence.
 *
 * `history` is chronological and its order is fixed server-side (no `order`
 * parameter — verified in the handler), so before/after is derived here from
 * consecutive entries and the list is reversed for display.
 */
export async function getTimeline(
  cfgId: string,
  opts: { cursor?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<Timeline> {
  const id = encodeURIComponent(cfgId);
  const limit = opts.limit ?? 50;
  const cursor = opts.cursor ? `&cursor=${encodeURIComponent(opts.cursor)}` : '';

  const history = await getJSON<HistoryResponse>(
    `/v1/governance/${id}/history?limit=${limit}${cursor}`,
    signal,
  );
  const items = history.items ?? [];

  // Hashes are supplementary: a failure here must not hide the governance record.
  let hashes = new Map<number, AuditEventItem>();
  try {
    const events = await getJSON<AuditEventsResponse>(
      `/v1/governance/${id}/audit/events?limit=${limit}${cursor}`,
      signal,
    );
    hashes = new Map((events.items ?? []).map((e) => [e.seq, e]));
  } catch {
    // Leave hashes empty; entries render without chain detail.
  }

  const chronological: AuditEntry[] = items.map((h, i) => {
    const e = hashes.get(h.seq);
    return {
      seq: h.seq,
      operation: h.operation,
      actor: h.actor,
      occurredAt: h.occurred_at,
      operationId: h.operation_id,
      removed: Boolean(h.removed),
      state: h.resulting_state,
      previousState: i > 0 ? items[i - 1].resulting_state : undefined,
      eventId: e?.event_id,
      prevHash: e?.prev_hash,
      thisHash: e?.this_hash,
    };
  });

  return { entries: chronological.reverse(), nextCursor: history.next_cursor };
}

/** Chain integrity summary. Absent on failure rather than blocking the timeline. */
export async function getChainSummary(
  cfgId: string,
  signal?: AbortSignal,
): Promise<ChainSummary | null> {
  try {
    return await getJSON<ChainSummary>(
      `/v1/governance/${encodeURIComponent(cfgId)}/audit`,
      signal,
    );
  } catch {
    return null;
  }
}
