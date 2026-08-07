import { useCallback, useEffect, useRef, useState } from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { getIncident } from '../incidents';
import type { IncidentDTO } from '../incidents';
import { humanise, isOpenIncidentStatus } from '../incidentPresentation';
import {
  getVulnFinding,
  legalVulnFindingTransitions,
  remediateVulnFinding,
  setVulnFindingStatus,
} from '../vulnerabilities';
import type { VulnFindingDTO, VulnFindingStatus } from '../vulnerabilities';
import { VULN_SEVERITY_TYPE, VULN_STATUS_TYPE } from '../vulnFindingPresentation';
import { ErrorState } from './States';

/** Label for a transition BUTTON, keyed by its TARGET status — mirrors IncidentDetail's TRANSITION_LABEL. */
const TRANSITION_LABEL: Partial<Record<VulnFindingStatus, string>> = {
  remediated: 'Mark remediated',
  accepted_risk: 'Accept risk',
  false_positive: 'Mark false positive',
  open: 'Reopen',
};

/**
 * Consequential transitions (ADR-ACT-001 §1 pattern): `accepted_risk` and
 * `false_positive` are operator JUDGMENTS about the finding's disposition —
 * confirmed via `Modal` before sending, mirroring UsersPage's terminal-
 * deactivate confirm. `remediated` is the routine, expected outcome of fixing
 * a real problem, and `open` (a manual re-triage) is a correction — neither
 * needs a second click.
 */
const CONSEQUENTIAL: Partial<Record<VulnFindingStatus, string>> = {
  accepted_risk:
    'This records a deliberate decision not to fix this finding right now. It reopens automatically if a later scan detects it again.',
  false_positive:
    'This marks the finding as a false alarm. Unlike other outcomes, a later scan reappearance will NOT reopen it ' +
    'automatically — only a manual re-triage back to Open (this same action) will.',
};

/**
 * The `SplitPanel` content for one vulnerability finding (E-SEC-UI.1): its
 * detail fields, legal-only status transitions, and one-click Remediate —
 * mirrors `IncidentDetailPanel`'s exact write-action shape (ADR-ACT-001):
 * disable-while-pending, refetch-after, 409-refetches-with-notice-never-
 * blind-retries, confirm-before-judgment. `onOpenIncident` lets the host open
 * the linked remediation incident through the SAME `SplitPanel` drill-down
 * idiom `AlertsBoardPage`'s "Linked incident" column already established,
 * rather than this panel owning a second `SplitPanel` of its own.
 */
export function VulnFindingDetailPanel({
  findingId,
  onChanged,
  onOpenIncident,
}: {
  findingId: string;
  onChanged?: () => void;
  onOpenIncident?: (incidentId: string) => void;
}) {
  const [finding, setFinding] = useState<VulnFindingDTO | null>(null);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [retries, setRetries] = useState(0);
  const prevFindingId = useRef<string | null>(null);

  // The linked remediation incident's own CURRENT status, fetched only to
  // decide whether Remediate should be offered again (ErrVulnFindingNotOpen
  // is not this row's concern — an OPEN link is). A fetch failure here (e.g.
  // no permission to read incidents) degrades to "unknown" rather than
  // blocking the finding's own detail from rendering.
  const [linkedIncident, setLinkedIncident] = useState<IncidentDTO | null>(null);

  const [busy, setBusy] = useState(false);
  const [actionProblem, setActionProblem] = useState<ProblemDetail | null>(null);
  const [conflictNotice, setConflictNotice] = useState(false);
  const [confirmTarget, setConfirmTarget] = useState<VulnFindingStatus | null>(null);
  const [remediatedIncident, setRemediatedIncident] = useState<IncidentDTO | null>(null);

  const refetch = useCallback(() => setRetries((n) => n + 1), []);

  useEffect(() => {
    const ctrl = new AbortController();
    const idChanged = prevFindingId.current !== findingId;
    prevFindingId.current = findingId;

    setLoading(true);
    setProblem(null);
    // A same-id refetch (post-mutation, or after a 409) must not blank the
    // detail already on screen — only switching to a DIFFERENT finding does,
    // the same rule IncidentDetailPanel follows.
    if (idChanged) {
      setFinding(null);
      setLinkedIncident(null);
      setRemediatedIncident(null);
    }

    getVulnFinding(findingId, ctrl.signal)
      .then((f) => {
        setFinding(f);
        if (f.remediation_incident_id) {
          // Supplementary — a failure here must not blank the finding detail.
          void getIncident(f.remediation_incident_id, ctrl.signal)
            .then((inc) => setLinkedIncident(inc))
            .catch(() => {});
        } else {
          setLinkedIncident(null);
        }
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(
          err instanceof ApiError ? err.problem : { title: 'Could not load finding', status: 0, detail: String(err) },
        );
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });

    return () => ctrl.abort();
  }, [findingId, retries]);

  /** Runs after ANY mutation attempt: success and 409 both refetch; only other errors leave the state as-is for the operator to read/retry. */
  const afterMutation = useCallback(
    (outcome: 'ok' | 'conflict') => {
      setConflictNotice(outcome === 'conflict');
      refetch();
      onChanged?.();
    },
    [refetch, onChanged],
  );

  const runTransition = useCallback(
    async (target: VulnFindingStatus) => {
      if (!finding) return;
      setBusy(true);
      setActionProblem(null);
      try {
        await setVulnFindingStatus(finding.finding_id, finding.row_version, target);
        setConfirmTarget(null);
        afterMutation('ok');
      } catch (err) {
        setConfirmTarget(null);
        if (err instanceof ApiError && err.problem.status === 409) {
          afterMutation('conflict');
        } else {
          setActionProblem(
            err instanceof ApiError ? err.problem : { title: 'Transition failed', status: 0, detail: String(err) },
          );
        }
      } finally {
        setBusy(false);
      }
    },
    [finding, afterMutation],
  );

  const runRemediate = useCallback(async () => {
    if (!finding) return;
    setBusy(true);
    setActionProblem(null);
    try {
      const result = await remediateVulnFinding(finding.finding_id, finding.row_version);
      setRemediatedIncident(result.incident);
      afterMutation('ok');
    } catch (err) {
      if (err instanceof ApiError && err.problem.status === 409) {
        afterMutation('conflict');
      } else {
        setActionProblem(
          err instanceof ApiError ? err.problem : { title: 'Remediate failed', status: 0, detail: String(err) },
        );
      }
    } finally {
      setBusy(false);
    }
  }, [finding, afterMutation]);

  const onTransitionClick = (target: VulnFindingStatus) => {
    setConflictNotice(false);
    if (CONSEQUENTIAL[target]) {
      setConfirmTarget(target);
      return;
    }
    void runTransition(target);
  };

  if (problem) return <ErrorState problem={problem} onRetry={refetch} />;

  if (loading && !finding) {
    return (
      <Box color="text-body-secondary" padding="l">
        Loading finding…
      </Box>
    );
  }

  if (!finding) return null;

  const transitions = legalVulnFindingTransitions(finding.status);
  // Remediate is only ever refused server-side for a non-open finding
  // (ErrVulnFindingNotOpen) — remediating an ALREADY-open-linked finding is
  // otherwise idempotent-safe (Remediate returns the same incident). This
  // additionally disables the button once an OPEN link is known, so an
  // operator is not invited to re-click something that would do nothing new.
  const remediationLinkedOpen = linkedIncident !== null && isOpenIncidentStatus(linkedIncident.status);
  const canRemediate = finding.status === 'open' && !remediationLinkedOpen;

  return (
    <SpaceBetween size="l">
      {conflictNotice && (
        <div role="alert">
          <Alert type="warning" header="Changed since you loaded it" dismissible onDismiss={() => setConflictNotice(false)}>
            This finding was modified concurrently. It has been reloaded to the current state below — review it and
            try again.
          </Alert>
        </div>
      )}

      {actionProblem && (
        <div role="alert">
          <Alert type="error" header="Action failed" dismissible onDismiss={() => setActionProblem(null)}>
            {actionProblem.detail ?? `The server returned ${actionProblem.status}.`}
          </Alert>
        </div>
      )}

      {remediatedIncident && (
        <div role="alert">
          <Alert type="success" header="Remediation incident linked" dismissible onDismiss={() => setRemediatedIncident(null)}>
            <Button variant="inline-link" onClick={() => onOpenIncident?.(remediatedIncident.incident_id)}>
              {remediatedIncident.incident_id}
            </Button>
          </Alert>
        </div>
      )}

      <SpaceBetween direction="horizontal" size="xs">
        {transitions.map((target) => (
          <Button
            key={target}
            disabled={busy}
            loading={busy && confirmTarget === null}
            onClick={() => onTransitionClick(target)}
          >
            {TRANSITION_LABEL[target] ?? humanise(target)}
          </Button>
        ))}
        <Button disabled={busy || !canRemediate} loading={busy} onClick={() => void runRemediate()}>
          Remediate
        </Button>
      </SpaceBetween>

      {!canRemediate && finding.status === 'open' && (
        <Box color="text-body-secondary" fontSize="body-s">
          Already linked to an open remediation incident.
        </Box>
      )}

      <KeyValuePairs
        columns={2}
        items={[
          { label: 'Title', value: finding.title },
          { label: 'Asset', value: finding.asset_id },
          { label: 'Vulnerability', value: finding.vuln_id },
          {
            label: 'Severity',
            value: (
              <StatusIndicator type={VULN_SEVERITY_TYPE[finding.severity]}>{humanise(finding.severity)}</StatusIndicator>
            ),
          },
          {
            label: 'Status',
            value: <StatusIndicator type={VULN_STATUS_TYPE[finding.status]}>{humanise(finding.status)}</StatusIndicator>,
          },
          { label: 'Scanner', value: finding.scanner },
          {
            label: 'Remediation',
            value: finding.remediation_incident_id ? (
              <Button variant="inline-link" onClick={() => onOpenIncident?.(finding.remediation_incident_id!)}>
                {finding.remediation_incident_id}
              </Button>
            ) : (
              <Box color="text-body-secondary">Not linked</Box>
            ),
          },
          { label: 'First seen', value: new Date(finding.first_seen).toLocaleString() },
          { label: 'Last seen', value: new Date(finding.last_seen).toLocaleString() },
          { label: 'Created', value: new Date(finding.created_at).toLocaleString() },
          { label: 'Updated', value: new Date(finding.updated_at).toLocaleString() },
        ]}
      />

      {finding.description && (
        <Box>
          <Box variant="awsui-key-label">Description</Box>
          <Box>{finding.description}</Box>
        </Box>
      )}

      {confirmTarget && (
        <Modal
          visible
          header={`${TRANSITION_LABEL[confirmTarget] ?? humanise(confirmTarget)} ${finding.finding_id}?`}
          onDismiss={() => {
            if (!busy) setConfirmTarget(null);
          }}
          closeAriaLabel="Dismiss"
          footer={
            <Box float="right">
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="link" onClick={() => setConfirmTarget(null)} disabled={busy}>
                  Cancel
                </Button>
                <Button variant="primary" loading={busy} onClick={() => void runTransition(confirmTarget)}>
                  {TRANSITION_LABEL[confirmTarget] ?? humanise(confirmTarget)}
                </Button>
              </SpaceBetween>
            </Box>
          }
        >
          <Box>{CONSEQUENTIAL[confirmTarget]}</Box>
        </Modal>
      )}
    </SpaceBetween>
  );
}
