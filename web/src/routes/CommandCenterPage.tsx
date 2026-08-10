import { useEffect, useMemo, useRef, useState } from 'react';
import type { ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import Grid from '@cloudscape-design/components/grid';
import type { GridProps } from '@cloudscape-design/components/grid';
import Header from '@cloudscape-design/components/header';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Link from '@cloudscape-design/components/link';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { humanise } from '../incidentPresentation';
import { getNOCOverview } from '../noc';
import type { NOCOverview } from '../noc';
import { listPrioritizedVulnFindings, VULN_FINDING_LIST_CAP } from '../vulnerabilities';
import type { PrioritizedVulnFindingDTO } from '../vulnerabilities';
import { VULN_PRIORITY_TYPE } from '../vulnFindingPresentation';
import { getRiskRegister, RISK_LIST_CAP } from '../risks';
import type { RiskRegisterEntryDTO } from '../risks';
import { RISK_SEVERITY_BAND_TYPE } from '../riskPresentation';
import { listComplianceControls, COMPLIANCE_CONTROL_LIST_CAP, COMPLIANCE_CONTROL_STATUSES } from '../complianceControls';
import type { ComplianceControlDTO, ComplianceControlStatus } from '../complianceControls';
import { COMPLIANCE_CONTROL_STATUS_TYPE } from '../compliancePresentation';

// Command Center (E-EXEC.1): a single projection screen composed ENTIRELY
// over existing admin endpoints (NOC overview, prioritized vuln findings, the
// risk register, and compliance controls). No new backend, no new table, no
// reified "Dashboard"/"Report" entity — the same computed-projection posture
// ADR-NOC-001 established for the NOC overview, applied across four already-
// existing domains rather than one. Each domain card fetches, loads and
// degrades INDEPENDENTLY: a 403/error on one card never blanks the others,
// the same "supplementary data must not fail the primary view" rule
// ADR-NOC-005's on-call board already follows for its own per-schedule
// fetches.

/** One grid cell per domain card: full width on phones, 2-up from "xs", matching NOCOverviewPage's own WIDGET_COLSPAN idiom. */
const CARD_COLSPAN: GridProps.ElementDefinition = { colspan: { default: 12, xs: 6 } };

/**
 * A count rendered through Cloudscape's semantic `StatusIndicator` — zero is
 * always `success` ("healthy"), a non-zero count takes the severity-
 * appropriate type the caller supplies. Mirrors NOCOverviewPage's own local
 * `Count` helper exactly (not exported there, so reproduced here rather than
 * reaching across a route module for a three-line function).
 */
function Count({ count, type }: { count: number; type: StatusIndicatorProps.Type }) {
  return <StatusIndicator type={count === 0 ? 'success' : type}>{count}</StatusIndicator>;
}

/** "Top N of M" when the list is longer than what's shown, an honest "All M" otherwise — never a fabricated total. */
function topNLabel(shown: number, total: number, noun: string): string {
  if (total === 0) return `No ${noun}.`;
  if (total <= shown) return `All ${total} ${noun}`;
  return `Top ${shown} of ${total} ${noun}`;
}

/**
 * Fetches once (and again on `retry()`), independent of every other card on
 * the page. `load` is read through a ref rather than a `useEffect` dependency
 * so a fresh inline closure passed by the caller on every render does not
 * retrigger the fetch — only `retry()` (or unmount/remount) does.
 */
function useCardPosture<T>(load: (signal: AbortSignal) => Promise<T>) {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloadToken, setReloadToken] = useState(0);
  const loadRef = useRef(load);
  loadRef.current = load;

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);
    loadRef
      .current(ctrl.signal)
      .then((d) => {
        if (!ctrl.signal.aborted) setData(d);
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(
          err instanceof ApiError ? err.problem : { title: 'Could not load', status: 0, detail: String(err) },
        );
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });
    return () => ctrl.abort();
  }, [reloadToken]);

  return { data, loading, problem, retry: () => setReloadToken((n) => n + 1) };
}

/**
 * The shared card chrome: a `Container` with a header, a "View all →" link to
 * the domain's own detail screen, and the three mutually-exclusive body
 * states (loading spinner / compact unavailable notice / content) every
 * domain card below plugs its own content into. The unavailable notice is
 * deliberately compact — no full-page `ErrorState` — so a 403 on this card
 * alone never dominates the pane the other cards are rendering into.
 */
function PostureCard({
  title,
  description,
  viewAllHref,
  loading,
  problem,
  unavailableMessage,
  onRetry,
  children,
}: {
  title: string;
  description: string;
  viewAllHref: string;
  loading: boolean;
  problem: ProblemDetail | null;
  unavailableMessage: string;
  onRetry: () => void;
  children: ReactNode;
}) {
  const navigate = useNavigate();
  return (
    <Container
      header={
        <Header
          variant="h2"
          description={description}
          actions={
            <Link
              href={viewAllHref}
              onFollow={(e) => {
                e.preventDefault();
                navigate(viewAllHref);
              }}
            >
              View all →
            </Link>
          }
        >
          {title}
        </Header>
      }
    >
      {loading && (
        <Box textAlign="center" padding="l">
          <div role="status" aria-busy="true">
            <Spinner /> Loading…
          </div>
        </Box>
      )}

      {!loading && problem && (
        <Box textAlign="center" padding="l">
          <SpaceBetween size="xs" alignItems="center">
            <StatusIndicator type="warning">{unavailableMessage}</StatusIndicator>
            <Button onClick={onRetry}>Retry</Button>
          </SpaceBetween>
        </Box>
      )}

      {!loading && !problem && children}
    </Container>
  );
}

/** Operations posture: `GET /v1/admin/noc/overview`, reused unchanged (ADR-NOC-001) — the on-call list already on the overview is reused as-is, no separate on-call fetch. */
function OperationsCard() {
  const { data, loading, problem, retry } = useCardPosture<NOCOverview>((signal) => getNOCOverview(signal));

  const assetIssueTotal = data
    ? data.assets.stale + data.assets.orphaned_assets + data.assets.orphaned_business_services + data.assets.incomplete
    : 0;

  return (
    <PostureCard
      title="Operations"
      description="Open incidents, firing alerts, and asset health across the NOC loop."
      viewAllHref="/noc"
      loading={loading}
      problem={problem}
      unavailableMessage="Operations posture unavailable"
      onRetry={retry}
    >
      {data && (
        <SpaceBetween size="m">
          <KeyValuePairs
            columns={2}
            items={[
              { label: 'Open incidents', value: <Count count={data.incidents.open_total} type="error" /> },
              { label: 'Alerts firing', value: <Count count={data.alerts.firing_total} type="error" /> },
            ]}
          />
          <div>
            <Box variant="awsui-key-label">Asset health</Box>
            {assetIssueTotal === 0 ? (
              <StatusIndicator type="success">All clear</StatusIndicator>
            ) : (
              <StatusIndicator type="warning">
                {assetIssueTotal} issue{assetIssueTotal === 1 ? '' : 's'}
              </StatusIndicator>
            )}
          </div>
          <div>
            <Box variant="awsui-key-label">On call now</Box>
            {data.on_call.length === 0 ? (
              <Box color="text-body-secondary" fontSize="body-s">
                No active on-call schedules.
              </Box>
            ) : (
              <SpaceBetween size="xs">
                {data.on_call.slice(0, 3).map((e) => (
                  <Box key={e.schedule_id} fontSize="body-s">
                    {e.schedule_name}: {e.display_name ?? 'Unassigned'}
                  </Box>
                ))}
                {data.on_call.length > 3 && (
                  <Box color="text-body-secondary" fontSize="body-s">
                    +{data.on_call.length - 3} more
                  </Box>
                )}
              </SpaceBetween>
            )}
          </div>
        </SpaceBetween>
      )}
    </PostureCard>
  );
}

/** Vulnerability posture: `GET /v1/admin/vuln-findings/prioritized` (E8.3b, ADR-SOC-007) — the caller's OPEN findings, already ranked by severity × asset criticality; this card just shows the count that endpoint returns and its own top 5. */
function VulnerabilitiesCard() {
  const { data, loading, problem, retry } = useCardPosture<{ items: PrioritizedVulnFindingDTO[] }>((signal) =>
    listPrioritizedVulnFindings(VULN_FINDING_LIST_CAP, signal),
  );
  const items = data?.items ?? [];
  const top5 = items.slice(0, 5);

  return (
    <PostureCard
      title="Vulnerabilities"
      description="Open findings ranked by severity × asset criticality."
      viewAllHref="/security/vulnerabilities"
      loading={loading}
      problem={problem}
      unavailableMessage="Vulnerability posture unavailable"
      onRetry={retry}
    >
      {data && (
        <SpaceBetween size="m">
          <KeyValuePairs columns={1} items={[{ label: 'Open findings', value: <Count count={items.length} type="error" /> }]} />
          <div>
            <Box variant="awsui-key-label">{topNLabel(5, items.length, 'open findings')}</Box>
            {top5.length === 0 ? (
              <Box color="text-body-secondary" fontSize="body-s">
                No open findings.
              </Box>
            ) : (
              <SpaceBetween size="xs">
                {top5.map((row) => (
                  <Box key={row.finding.finding_id} fontSize="body-s">
                    <StatusIndicator type={VULN_PRIORITY_TYPE[row.priority]}>{humanise(row.priority)}</StatusIndicator>{' '}
                    {row.finding.asset_id} · {row.finding.vuln_id}
                  </Box>
                ))}
              </SpaceBetween>
            )}
          </div>
          {items.length >= VULN_FINDING_LIST_CAP && (
            <Box color="text-status-warning" fontSize="body-s">
              Showing the first {VULN_FINDING_LIST_CAP} open findings — see the Vulnerabilities screen for the rest.
            </Box>
          )}
        </SpaceBetween>
      )}
    </PostureCard>
  );
}

/** Risk posture: `GET /v1/admin/risks/register` (E8.4a, ADR-SOC-008) — the caller's OPEN (non-closed) risks, already ranked by computed likelihood × impact score. */
function RiskCard() {
  const { data, loading, problem, retry } = useCardPosture<{ items: RiskRegisterEntryDTO[] }>((signal) =>
    getRiskRegister(RISK_LIST_CAP, signal),
  );
  const items = data?.items ?? [];
  const top5 = items.slice(0, 5);

  return (
    <PostureCard
      title="Risk"
      description="Open risks ranked by likelihood × impact score."
      viewAllHref="/security/risks"
      loading={loading}
      problem={problem}
      unavailableMessage="Risk posture unavailable"
      onRetry={retry}
    >
      {data && (
        <SpaceBetween size="m">
          <KeyValuePairs columns={1} items={[{ label: 'Open risks', value: <Count count={items.length} type="error" /> }]} />
          <div>
            <Box variant="awsui-key-label">{topNLabel(5, items.length, 'open risks')}</Box>
            {top5.length === 0 ? (
              <Box color="text-body-secondary" fontSize="body-s">
                No open risks.
              </Box>
            ) : (
              <SpaceBetween size="xs">
                {top5.map((row) => (
                  <Box key={row.risk.risk_id} fontSize="body-s">
                    <StatusIndicator type={RISK_SEVERITY_BAND_TYPE[row.band]}>{humanise(row.band)}</StatusIndicator>{' '}
                    {row.risk.title}
                  </Box>
                ))}
              </SpaceBetween>
            )}
          </div>
          {items.length >= RISK_LIST_CAP && (
            <Box color="text-status-warning" fontSize="body-s">
              Showing the first {RISK_LIST_CAP} open risks — see the Risk register screen for the rest.
            </Box>
          )}
        </SpaceBetween>
      )}
    </PostureCard>
  );
}

/** Compliance posture: `GET /v1/admin/compliance-controls` (E8.4b, ADR-SOC-009), unfiltered — one bounded page, tallied client-side by status. */
function ComplianceCard() {
  const { data, loading, problem, retry } = useCardPosture<{ items: ComplianceControlDTO[] }>((signal) =>
    listComplianceControls({ limit: COMPLIANCE_CONTROL_LIST_CAP }, signal),
  );
  const items = data?.items ?? [];

  const counts = useMemo(() => {
    const tally: Record<ComplianceControlStatus, number> = {
      not_implemented: 0,
      in_progress: 0,
      implemented: 0,
      not_applicable: 0,
    };
    for (const c of items) tally[c.status] += 1;
    return tally;
  }, [items]);

  return (
    <PostureCard
      title="Compliance"
      description="Control implementation status across your frameworks."
      viewAllHref="/security/compliance"
      loading={loading}
      problem={problem}
      unavailableMessage="Compliance posture unavailable"
      onRetry={retry}
    >
      {data && (
        <SpaceBetween size="m">
          <KeyValuePairs
            columns={2}
            items={COMPLIANCE_CONTROL_STATUSES.map((s) => ({
              label: humanise(s),
              value: <StatusIndicator type={COMPLIANCE_CONTROL_STATUS_TYPE[s]}>{counts[s]}</StatusIndicator>,
            }))}
          />
          {items.length >= COMPLIANCE_CONTROL_LIST_CAP && (
            <Box color="text-status-warning" fontSize="body-s">
              Counts reflect the first {COMPLIANCE_CONTROL_LIST_CAP} controls returned — see the Compliance screen for
              a complete view of a larger set.
            </Box>
          )}
        </SpaceBetween>
      )}
    </PostureCard>
  );
}

/**
 * Command Center (E-EXEC.1): the executive landing page — one pane
 * summarizing operational (NOC), vulnerability, risk, and compliance posture
 * in one screen, purely composed over the four admin endpoints those
 * domains' own boards already use. A PROJECTION, not a reified entity: this
 * page stores nothing and adds no backend surface (Vol III §3.4's own
 * "Dashboard/Report are derived, never stored" rule, the same one
 * ADR-NOC-001 already applied to the NOC overview). Each of the four cards
 * below fetches, loads, and degrades entirely independently — a 403 or a
 * transient failure on one domain's endpoint never prevents the other three
 * from rendering their own posture.
 */
export function CommandCenterPage() {
  return (
    <SpaceBetween size="l">
      <Header variant="h1" description="At-a-glance operational, security, and governance posture across the platform.">
        Command Center
      </Header>

      <Grid gridDefinition={[CARD_COLSPAN, CARD_COLSPAN, CARD_COLSPAN, CARD_COLSPAN]}>
        <OperationsCard />
        <VulnerabilitiesCard />
        <RiskCard />
        <ComplianceCard />
      </Grid>
    </SpaceBetween>
  );
}
