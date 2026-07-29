import { useCallback, useEffect, useState } from 'react';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { getChainSummary, getTimeline } from '../audit';
import type { AuditEntry, ChainSummary, ResultingState } from '../audit';
import { ErrorState } from './States';

const humanise = (v?: string) =>
  (v ?? '').replace(/_/g, ' ').replace(/^\w/, (c) => c.toUpperCase());

/** Absolute time is the evidence; relative time is the convenience. Show both. */
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

/** Only the dimensions that actually moved, so the change is legible at a glance. */
function changes(prev: ResultingState | undefined, next: ResultingState | undefined) {
  if (!next) return [];
  const rows: Array<{ dim: string; from?: string; to: string }> = [];
  const dims = [
    ['Lifecycle', prev?.lifecycle, next.lifecycle],
    ['Authority', prev?.authority, next.authority],
    ['Retention', prev?.retention_class, next.retention_class],
  ] as const;

  for (const [dim, from, to] of dims) {
    if (from !== to) rows.push({ dim, from: from ? humanise(from) : undefined, to: humanise(to) });
  }
  return rows;
}

function Entry({ entry }: { entry: AuditEntry }) {
  const [open, setOpen] = useState(false);
  const t = when(entry.occurredAt);
  const moved = changes(entry.previousState, entry.state);

  return (
    <li className="event">
      <div className="event-head">
        <span className="op-badge">{humanise(entry.operation)}</span>
        <span className="event-actor">{entry.actor}</span>
        <time dateTime={entry.occurredAt} title={t.absolute} className="event-when">
          {t.relative}
        </time>
        <span className="event-seq" title="Position in the immutable audit chain">
          #{entry.seq}
        </span>
      </div>

      {entry.removed ? (
        <p className="event-change">Object removed.</p>
      ) : moved.length > 0 ? (
        <div className="event-change">
          {moved.map((c) => (
            <div key={c.dim}>
              <span className="dim-name">{c.dim}</span>{' '}
              {c.from ? (
                <>
                  <span className="from">{c.from}</span> <span aria-hidden="true">→</span>{' '}
                  <span className="sr-only">changed to</span>
                  <span className="to">{c.to}</span>
                </>
              ) : (
                <span className="to">{c.to}</span>
              )}
            </div>
          ))}
        </div>
      ) : (
        <p className="event-change muted">No dimension changed.</p>
      )}

      <button
        type="button"
        className="link event-more"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
      >
        {open ? 'Hide' : 'Show'} chain record
      </button>

      {open && (
        <dl className="event-detail">
          <div>
            <dt>Occurred</dt>
            <dd>{t.absolute}</dd>
          </div>
          <div>
            <dt>Operation id</dt>
            <dd className="hash">{entry.operationId}</dd>
          </div>
          {entry.eventId && (
            <div>
              <dt>Event id</dt>
              <dd className="hash">{entry.eventId}</dd>
            </div>
          )}
          {entry.thisHash && (
            <div>
              <dt>This hash</dt>
              <dd className="hash">{entry.thisHash}</dd>
            </div>
          )}
          {entry.prevHash && (
            <div>
              <dt>Previous hash</dt>
              <dd className="hash">{entry.prevHash}</dd>
            </div>
          )}
        </dl>
      )}
    </li>
  );
}

export function AuditTimeline({ cfgId, refreshToken }: { cfgId: string; refreshToken: number }) {
  const [entries, setEntries] = useState<AuditEntry[]>([]);
  const [chain, setChain] = useState<ChainSummary | null>(null);
  const [cursor, setCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [retries, setRetries] = useState(0);

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);

    getTimeline(cfgId, {}, ctrl.signal)
      .then((t) => {
        setEntries(t.entries);
        setCursor(t.nextCursor);
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load the audit record', status: 0, detail: String(err) },
        );
        setEntries([]);
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });

    void getChainSummary(cfgId, ctrl.signal).then((c) => {
      if (!ctrl.signal.aborted) setChain(c);
    });

    return () => ctrl.abort();
  }, [cfgId, refreshToken, retries]);

  const loadMore = useCallback(() => {
    setLoading(true);
    getTimeline(cfgId, { cursor })
      .then((t) => {
        setEntries((prev) => [...prev, ...t.entries]);
        setCursor(t.nextCursor);
      })
      .catch(() => {
        /* keep what is already shown */
      })
      .finally(() => setLoading(false));
  }, [cfgId, cursor]);

  return (
    <section className="panel">
      <h3>
        Audit record
        {chain && (
          <span
            className={chain.verified ? 'chain-ok' : 'chain-broken'}
            title={
              chain.verified
                ? `Hash chain verified across ${chain.checked} event(s).`
                : chain.break_reason ?? 'Chain integrity could not be confirmed.'
            }
          >
            {chain.verified ? '✓ Chain verified' : '⚠ Chain unverified'}
          </span>
        )}
      </h3>
      <p className="panel-hint">
        Every governance operation, append-only and hash-chained. Newest first.
      </p>

      {problem && <ErrorState problem={problem} onRetry={() => setRetries((n) => n + 1)} />}

      {!problem && loading && entries.length === 0 && (
        <p className="muted" role="status" aria-busy="true">
          Loading the audit record…
        </p>
      )}

      {!problem && !loading && entries.length === 0 && (
        <p className="muted">
          No governance operation has been performed on this artifact yet.
        </p>
      )}

      {entries.length > 0 && (
        <>
          <ol className="events">
            {entries.map((e) => (
              <Entry key={`${e.seq}-${e.operationId}`} entry={e} />
            ))}
          </ol>
          {cursor && (
            <div className="more">
              <button type="button" className="btn" onClick={loadMore} disabled={loading}>
                {loading ? 'Loading…' : 'Load older'}
              </button>
            </div>
          )}
        </>
      )}
    </section>
  );
}
