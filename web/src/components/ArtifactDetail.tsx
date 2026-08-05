import { useCallback, useEffect, useState } from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import Header from '@cloudscape-design/components/header';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import SpaceBetween from '@cloudscape-design/components/space-between';
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
    <section>
      <Container header={<Header variant="h3" description={hint} counter={`(${data.total})`}>{title}</Header>}>
        {data.relations.length === 0 ? (
          <Box color="text-body-secondary">None.</Box>
        ) : (
          <ul style={{ listStyle: 'none', margin: 0, padding: 0, display: 'flex', flexDirection: 'column', gap: 8 }}>
            {data.relations.map((r) => (
              <li key={r.cfg_id} style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12 }}>
                <Button variant="inline-link" onClick={() => onOpen(r.cfg_id)}>
                  {r.artifact ?? r.cfg_id}
                </Button>
                {r.authority && <AuthorityPill value={r.authority} />}
              </li>
            ))}
            {data.total > data.relations.length && (
              <li>
                <Box color="text-body-secondary">
                  …and {data.total - data.relations.length} more, not shown.
                </Box>
              </li>
            )}
          </ul>
        )}
      </Container>
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
    <SpaceBetween size="l">
      <Button variant="link" onClick={onBack} iconName="angle-left">
        Back to estate
      </Button>

      {problem && <ErrorState problem={problem} onRetry={retry} />}

      {loading && !problem && (
        <Box color="text-body-secondary" textAlign="center" padding="l">
          Loading artifact…
        </Box>
      )}

      {artifact && (
        <SpaceBetween size="l">
          <Header
            variant="h1"
            description={`${artifact.cfg_id} · version ${artifact.version}`}
            actions={
              RATIFY.available(artifact) ? (
                <Button
                  variant="primary"
                  onClick={() => {
                    setOpProblem(null);
                    setOutcome(null);
                    setConfirming({ key: newIdempotencyKey() });
                  }}
                >
                  {RATIFY.label}
                </Button>
              ) : (
                <Box color="text-body-secondary" fontSize="body-s" nativeAttributes={{ title: RATIFY.unavailableBecause(artifact) }}>
                  {RATIFY.label} unavailable
                </Box>
              )
            }
          >
            <SpaceBetween direction="horizontal" size="s" alignItems="center">
              <span>{artifact.artifact}</span>
              <AuthorityPill value={artifact.authority} />
            </SpaceBetween>
          </Header>

          {outcome?.state && (
            <div role="status">
              <Alert type="success" header="Ratified.">
                This artifact now governs — lifecycle {outcome.state.lifecycle.replace(/_/g, ' ')},
                authority {outcome.state.authority.replace(/_/g, ' ')}, retention{' '}
                {outcome.state.retention_class.replace(/_/g, ' ')}. Recorded in the audit chain by{' '}
                {outcome.actor}.
              </Alert>
            </div>
          )}

          <section>
            <Container
              header={
                <Header variant="h3" description="Four independent dimensions. Authority is computed from the dependency graph; the others are asserted.">
                  Classification
                </Header>
              }
            >
              <KeyValuePairs
                columns={4}
                items={[
                  { label: 'Authority', value: <AuthorityPill value={artifact.authority} /> },
                  { label: 'Lifecycle', value: humanise(artifact.lifecycle) },
                  { label: 'Retention', value: humanise(artifact.retention_class) },
                  { label: 'Role', value: humanise(artifact.role) },
                ]}
              />
            </Container>
          </section>

          <ColumnLayout columns={2}>
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
          </ColumnLayout>

          <section>
            <Container>
              <AuditTimeline cfgId={artifact.cfg_id} refreshToken={reloads} />
            </Container>
          </section>

          <Container header={<Header variant="h3">Record</Header>}>
            <KeyValuePairs
              columns={3}
              items={[
                ...(artifact.ratified_by ? [{ label: 'Ratified by', value: artifact.ratified_by }] : []),
                ...(artifact.review_cycle ? [{ label: 'Review cycle', value: artifact.review_cycle }] : []),
                ...(artifact.retention_policy
                  ? [{ label: 'Retention policy', value: artifact.retention_policy }]
                  : []),
                { label: 'Created', value: when(artifact.created_at) },
                { label: 'Updated', value: when(artifact.updated_at) },
                { label: 'Revision', value: String(artifact.row_version) },
              ]}
            />
          </Container>

          {artifact.metadata && Object.keys(artifact.metadata).length > 0 && (
            <Container header={<Header variant="h3">Metadata</Header>}>
              <KeyValuePairs
                columns={3}
                items={Object.entries(artifact.metadata).map(([k, v]) => ({ label: k, value: v }))}
              />
            </Container>
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
        </SpaceBetween>
      )}
    </SpaceBetween>
  );
}
