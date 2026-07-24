import { useCallback, useEffect, useState } from 'react';
import { ApiError, executeGovernance, getArtifact, getRelations, newIdempotencyKey } from '../api';
import type { ConfigObject, GovernanceResult, ProblemDetail, Relation } from '../api';
import { RATIFY } from '../governance';
import { AuditTimeline } from './AuditTimeline';
import { AuthorityPill } from './AuthorityPill';
import { ConfirmOperation } from './ConfirmOperation';
import { ErrorState } from './States';

const humanise = (v: string) => v.replace(/_/g, ' ').replace(/^\w/, (c) => c.toUpperCase());
const when = (iso: string) => new Date(iso).toLocaleString();

interface Relations {
  total: number;
  relations: Relation[];
}

function RelationList({
  title,
  hint,
  data,
  onOpen,
}: {
  title: string;
  hint: string;
  data?: Relations;
  onOpen: (id: string) => void;
}) {
  if (!data) return null;

  return (
    <section className="panel">
      <h3>
        {title} <span className="count">{data.total}</span>
      </h3>
      <p className="panel-hint">{hint}</p>
      {data.relations.length === 0 ? (
        <p className="muted">None.</p>
      ) : (
        <ul className="relations">
          {data.relations.map((r) => (
            <li key={r.cfg_id}>
              <button type="button" className="link" onClick={() => onOpen(r.cfg_id)}>
                {r.artifact ?? r.cfg_id}
              </button>
              {r.authority && <AuthorityPill value={r.authority} />}
            </li>
          ))}
          {data.total > data.relations.length && (
            <li className="muted">
              …and {data.total - data.relations.length} more, not shown.
            </li>
          )}
        </ul>
      )}
    </section>
  );
}

export function ArtifactDetail({
  cfgId,
  onBack,
  onOpen,
}: {
  cfgId: string;
  onBack: () => void;
  onOpen: (id: string) => void;
}) {
  const [artifact, setArtifact] = useState<ConfigObject | null>(null);
  const [deps, setDeps] = useState<Relations>();
  const [dependents, setDependents] = useState<Relations>();
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);

  // Governance operation state. The idempotency key is minted when the dialog
  // opens and reused across retries, so a retry is the same intent, not a new one.
  const [confirming, setConfirming] = useState<{ key: string } | null>(null);
  const [opBusy, setOpBusy] = useState(false);
  const [opProblem, setOpProblem] = useState<ProblemDetail | null>(null);
  const [outcome, setOutcome] = useState<GovernanceResult | null>(null);

  const retry = useCallback(() => setReloads((n) => n + 1), []);

  const confirmOperation = useCallback(async () => {
    if (!artifact || !confirming) return;
    setOpBusy(true);
    setOpProblem(null);
    try {
      const result = await executeGovernance(artifact.cfg_id, RATIFY.id, {
        rowVersion: artifact.row_version,
        idempotencyKey: confirming.key,
      });
      setOutcome(result);
      setConfirming(null);
      setReloads((n) => n + 1); // refresh artifact and relations from the server
    } catch (err) {
      setOpProblem(
        err instanceof ApiError
          ? err.problem
          : { title: 'Operation failed', status: 0, detail: String(err) },
      );
    } finally {
      setOpBusy(false);
    }
  }, [artifact, confirming]);

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);
    setArtifact(null);
    setDeps(undefined);
    setDependents(undefined);

    // The artifact drives the view; relations are supplementary and must never
    // fail it — the graph may legitimately be empty or unavailable.
    getArtifact(cfgId, ctrl.signal)
      .then((o) => {
        setArtifact(o);
        void getRelations(cfgId, 'dependencies', ctrl.signal).then(setDeps).catch(() => {});
        void getRelations(cfgId, 'dependents', ctrl.signal).then(setDependents).catch(() => {});
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load artifact', status: 0, detail: String(err) },
        );
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });

    return () => ctrl.abort();
  }, [cfgId, reloads]);

  return (
    <div className="detail">
      <button type="button" className="link back" onClick={onBack}>
        ← Back to estate
      </button>

      {problem && <ErrorState problem={problem} onRetry={retry} />}

      {loading && !problem && (
        <div className="state" role="status" aria-busy="true">
          Loading artifact…
        </div>
      )}

      {artifact && (
        <>
          <header className="detail-head">
            <h2>{artifact.artifact}</h2>
            <AuthorityPill value={artifact.authority} />
            <div className="actions">
              {RATIFY.available(artifact) ? (
                <button
                  type="button"
                  className="btn btn-primary"
                  onClick={() => {
                    setOpProblem(null);
                    setOutcome(null);
                    setConfirming({ key: newIdempotencyKey() });
                  }}
                >
                  {RATIFY.label}
                </button>
              ) : (
                <span className="unavailable" title={RATIFY.unavailableBecause(artifact)}>
                  {RATIFY.label} unavailable
                </span>
              )}
            </div>
          </header>
          <p className="cfg-id">
            {artifact.cfg_id} · version {artifact.version}
          </p>

          {outcome?.state && (
            <div className="outcome" role="status">
              <strong>Ratified.</strong> This artifact now governs — lifecycle{' '}
              {outcome.state.lifecycle.replace(/_/g, ' ')}, authority{' '}
              {outcome.state.authority.replace(/_/g, ' ')}, retention{' '}
              {outcome.state.retention_class.replace(/_/g, ' ')}. Recorded in the audit
              chain by {outcome.actor}.
            </div>
          )}

          <section className="panel">
            <h3>Classification</h3>
            <p className="panel-hint">
              Four independent dimensions. Authority is computed from the dependency
              graph; the others are asserted.
            </p>
            <dl className="dims">
              <div>
                <dt>Authority</dt>
                <dd>
                  <AuthorityPill value={artifact.authority} />
                </dd>
              </div>
              <div>
                <dt>Lifecycle</dt>
                <dd>{humanise(artifact.lifecycle)}</dd>
              </div>
              <div>
                <dt>Retention</dt>
                <dd>{humanise(artifact.retention_class)}</dd>
              </div>
              <div>
                <dt>Role</dt>
                <dd>{humanise(artifact.role)}</dd>
              </div>
            </dl>
          </section>

          <div className="two-col">
            <RelationList
              title="Depends on"
              hint="Artifacts this one relies upon."
              data={deps}
              onOpen={onOpen}
            />
            <RelationList
              title="Depended on by"
              hint="What breaks if this artifact changes."
              data={dependents}
              onOpen={onOpen}
            />
          </div>

          <AuditTimeline cfgId={artifact.cfg_id} refreshToken={reloads} />

          <section className="panel">
            <h3>Record</h3>
            <dl className="dims">
              {artifact.ratified_by && (
                <div>
                  <dt>Ratified by</dt>
                  <dd>{artifact.ratified_by}</dd>
                </div>
              )}
              {artifact.review_cycle && (
                <div>
                  <dt>Review cycle</dt>
                  <dd>{artifact.review_cycle}</dd>
                </div>
              )}
              {artifact.retention_policy && (
                <div>
                  <dt>Retention policy</dt>
                  <dd>{artifact.retention_policy}</dd>
                </div>
              )}
              <div>
                <dt>Created</dt>
                <dd>{when(artifact.created_at)}</dd>
              </div>
              <div>
                <dt>Updated</dt>
                <dd>{when(artifact.updated_at)}</dd>
              </div>
              <div>
                <dt>Revision</dt>
                <dd>{artifact.row_version}</dd>
              </div>
            </dl>
          </section>

          {artifact.metadata && Object.keys(artifact.metadata).length > 0 && (
            <section className="panel">
              <h3>Metadata</h3>
              <dl className="dims">
                {Object.entries(artifact.metadata).map(([k, v]) => (
                  <div key={k}>
                    <dt>{k}</dt>
                    <dd>{v}</dd>
                  </div>
                ))}
              </dl>
            </section>
          )}

          {confirming && (
            <ConfirmOperation
              operation={RATIFY}
              artifact={artifact}
              busy={opBusy}
              problem={opProblem}
              onConfirm={confirmOperation}
              onCancel={() => {
                setConfirming(null);
                setOpProblem(null);
              }}
            />
          )}
        </>
      )}
    </div>
  );
}
