import { useEffect, useState } from 'react';
import Box from '@cloudscape-design/components/box';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { ALERT_SEVERITY_TYPE, ALERT_STATE_TYPE, COMPARATOR_SYMBOL } from '../alertPresentation';
import { getAlertRule } from '../alertRules';
import type { AlertRuleDTO } from '../alertRules';
import { humanise } from '../incidentPresentation';
import { ErrorState } from './States';

/**
 * The `SplitPanel` content for one alert rule, reusing `GET
 * /v1/admin/alert-rules/{id}` unchanged — the same load/error structure
 * `IncidentDetailPanel` establishes. No timeline exists for a rule (unlike
 * an incident); this is a single-fetch detail view.
 */
export function AlertRuleDetailPanel({ ruleId }: { ruleId: string }) {
  const [rule, setRule] = useState<AlertRuleDTO | null>(null);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [retries, setRetries] = useState(0);

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);
    setRule(null);

    getAlertRule(ruleId, ctrl.signal)
      .then(setRule)
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load alert rule', status: 0, detail: String(err) },
        );
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });

    return () => ctrl.abort();
  }, [ruleId, retries]);

  if (problem) return <ErrorState problem={problem} onRetry={() => setRetries((n) => n + 1)} />;

  if (loading && !rule) {
    return (
      <Box color="text-body-secondary" padding="l">
        Loading alert rule…
      </Box>
    );
  }

  if (!rule) return null;

  return (
    <SpaceBetween size="l">
      <KeyValuePairs
        columns={2}
        items={[
          { label: 'Asset', value: rule.asset_id },
          { label: 'Metric', value: rule.metric },
          {
            label: 'Severity',
            value: <StatusIndicator type={ALERT_SEVERITY_TYPE[rule.severity]}>{humanise(rule.severity)}</StatusIndicator>,
          },
          {
            label: 'State',
            value: <StatusIndicator type={ALERT_STATE_TYPE[rule.last_state]}>{humanise(rule.last_state)}</StatusIndicator>,
          },
          { label: 'Threshold', value: `${COMPARATOR_SYMBOL[rule.comparator]} ${rule.threshold}` },
          { label: 'For duration', value: `${rule.for_duration_seconds}s` },
          { label: 'Symptom class', value: humanise(rule.symptom_class) },
          {
            label: 'Enabled',
            value: <StatusIndicator type={rule.enabled ? 'success' : 'stopped'}>{rule.enabled ? 'Enabled' : 'Disabled'}</StatusIndicator>,
          },
          { label: 'Flap dwell', value: `${rule.flap_dwell_seconds}s` },
          {
            label: 'Last transition',
            value: rule.last_transition_at
              ? new Date(rule.last_transition_at).toLocaleString()
              : <Box color="text-body-secondary">Never evaluated</Box>,
          },
          { label: 'Created', value: new Date(rule.created_at).toLocaleString() },
          { label: 'Updated', value: new Date(rule.updated_at).toLocaleString() },
        ]}
      />
      <Box color="text-body-secondary" fontSize="body-s">
        This rule's linked incident (if any) is not part of the current alert-rule
        contract — see ADR-NOC-005.
      </Box>
    </SpaceBetween>
  );
}
