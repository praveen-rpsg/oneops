import { useCallback, useEffect, useState } from 'react';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Header from '@cloudscape-design/components/header';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
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
    <li style={{ padding: '12px 0', borderBottom: '1px solid var(--awsui-color-border-divider-default, #e2e5ea)' }}>
      <SpaceBetween size="xs">
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 12, flexWrap: 'wrap' }}>
          <Box variant="strong">{humanise(entry.operation)}</Box>
          <Box>{entry.actor}</Box>
          <time dateTime={entry.occurredAt} title={t.absolute}>
            <Box color="text-body-secondary" fontSize="body-s" display="inline">
              {t.relative}
            </Box>
          </time>
          <Box
            color="text-body-secondary"
            fontSize="body-s"
            float="right"
            nativeAttributes={{ title: 'Position in the immutable audit chain' }}
          >
            #{entry.seq}
          </Box>
        </div>

        {entry.removed ? (
          <Box>Object removed.</Box>
        ) : moved.length > 0 ? (
          <SpaceBetween size="xxs">
            {moved.map((c) => (
              <div key={c.dim}>
                <Box color="text-body-secondary" display="inline">
                  {c.dim}
                </Box>{' '}
                {c.from ? (
                  <>
                    <Box display="inline" color="text-body-secondary">
                      <s>{c.from}</s>
                    </Box>{' '}
                    <span aria-hidden="true">→</span>{' '}
                    <span className="visually-hidden">changed to</span>
                    <Box display="inline" fontWeight="bold">
                      {c.to}
                    </Box>
                  </>
                ) : (
                  <Box display="inline" fontWeight="bold">
                    {c.to}
                  </Box>
                )}
              </div>
            ))}
          </SpaceBetween>
        ) : (
          <Box color="text-body-secondary">No dimension changed.</Box>
        )}

        <Button
          variant="inline-link"
          ariaExpanded={open}
          onClick={() => setOpen((v) => !v)}
        >
          {open ? 'Hide' : 'Show'} chain record
        </Button>

        {open && (
          <dl style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))', gap: '8px 16px', margin: 0 }}>
            <div>
              <dt><Box color="text-body-secondary" fontSize="body-s">Occurred</Box></dt>
              <dd style={{ margin: 0 }}>{t.absolute}</dd>
            </div>
            <div>
              <dt><Box color="text-body-secondary" fontSize="body-s">Operation id</Box></dt>
              <dd style={{ margin: 0, fontFamily: 'monospace', wordBreak: 'break-all' }}>{entry.operationId}</dd>
            </div>
            {entry.eventId && (
              <div>
                <dt><Box color="text-body-secondary" fontSize="body-s">Event id</Box></dt>
                <dd style={{ margin: 0, fontFamily: 'monospace', wordBreak: 'break-all' }}>{entry.eventId}</dd>
              </div>
            )}
            {entry.thisHash && (
              <div>
                <dt><Box color="text-body-secondary" fontSize="body-s">This hash</Box></dt>
                <dd style={{ margin: 0, fontFamily: 'monospace', wordBreak: 'break-all' }}>{entry.thisHash}</dd>
              </div>
            )}
            {entry.prevHash && (
              <div>
                <dt><Box color="text-body-secondary" fontSize="body-s">Previous hash</Box></dt>
                <dd style={{ margin: 0, fontFamily: 'monospace', wordBreak: 'break-all' }}>{entry.prevHash}</dd>
              </div>
            )}
          </dl>
        )}
      </SpaceBetween>
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
    <SpaceBetween size="s">
      <Header
        variant="h3"
        description="Every governance operation, append-only and hash-chained. Newest first."
        info={
          chain ? (
            <StatusIndicator
              type={chain.verified ? 'success' : 'warning'}
              nativeAttributes={{
                title: chain.verified
                  ? `Hash chain verified across ${chain.checked} event(s).`
                  : chain.break_reason ?? 'Chain integrity could not be confirmed.',
              }}
            >
              {chain.verified ? '✓ Chain verified' : '⚠ Chain unverified'}
            </StatusIndicator>
          ) : undefined
        }
      >
        Audit record
      </Header>

      {problem && <ErrorState problem={problem} onRetry={() => setRetries((n) => n + 1)} />}

      {!problem && loading && entries.length === 0 && (
        <Box color="text-body-secondary">Loading the audit record…</Box>
      )}

      {!problem && !loading && entries.length === 0 && (
        <Box color="text-body-secondary">
          No governance operation has been performed on this artifact yet.
        </Box>
      )}

      {entries.length > 0 && (
        <>
          <ol style={{ listStyle: 'none', margin: 0, padding: 0 }}>
            {entries.map((e) => (
              <Entry key={`${e.seq}-${e.operationId}`} entry={e} />
            ))}
          </ol>
          {cursor && (
            <Box textAlign="center">
              <Button onClick={loadMore} loading={loading}>
                Load older
              </Button>
            </Box>
          )}
        </>
      )}
    </SpaceBetween>
  );
}
