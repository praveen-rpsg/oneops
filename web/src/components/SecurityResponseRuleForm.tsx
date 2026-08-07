import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Select from '@cloudscape-design/components/select';
import type { SelectProps } from '@cloudscape-design/components/select';
import Textarea from '@cloudscape-design/components/textarea';
import Toggle from '@cloudscape-design/components/toggle';
import { humanise } from '../incidentPresentation';
import { INCIDENT_SEVERITIES } from '../securityResponseRules';
import type { IncidentSeverity } from '../securityResponseRules';

// The fields `patchSecurityResponseRuleRequest` accepts (E-SEC-UI.4), shared
// by the create modal (routes/ResponseRulesPage.tsx — which additionally
// collects asset_id/action_type, fixed at creation) and the edit modal
// (components/SecurityResponseRuleDetail.tsx — which does not, since
// domain.SecurityResponseRulePatch cannot touch them). min_severity renders
// as a Select constrained to the real backend value set, so a bad enum is
// impossible to submit through this form.

const SEVERITY_OPTIONS: SelectProps.Option[] = INCIDENT_SEVERITIES.map((s) => ({ value: s, label: humanise(s) }));

export interface SecurityResponseRuleConfigValues {
  name: string;
  minSeverity: IncidentSeverity;
  enabled: boolean;
}

export interface SecurityResponseRuleConfigErrors {
  name?: string;
}

export function SecurityResponseRuleConfigFields({
  values,
  onChange,
  errors,
  disabled,
}: {
  values: SecurityResponseRuleConfigValues;
  onChange: (values: SecurityResponseRuleConfigValues) => void;
  errors: SecurityResponseRuleConfigErrors;
  disabled: boolean;
}) {
  return (
    <>
      <FormField label="Name" errorText={errors.name}>
        <Input
          value={values.name}
          onChange={({ detail }) => onChange({ ...values, name: detail.value })}
          disabled={disabled}
          ariaLabel="Name"
          placeholder="Required"
        />
      </FormField>
      <FormField label="Minimum severity" description="Fire when a matching security incident's severity is at least this">
        <Select
          selectedOption={SEVERITY_OPTIONS.find((o) => o.value === values.minSeverity) ?? SEVERITY_OPTIONS[0]}
          onChange={({ detail }) => onChange({ ...values, minSeverity: (detail.selectedOption.value ?? 'low') as IncidentSeverity })}
          options={SEVERITY_OPTIONS}
          disabled={disabled}
          ariaLabel="Minimum severity"
        />
      </FormField>
      <FormField label="Enabled">
        <Toggle checked={values.enabled} onChange={({ detail }) => onChange({ ...values, enabled: detail.checked })} disabled={disabled}>
          {values.enabled ? 'Enabled' : 'Disabled'}
        </Toggle>
      </FormField>
    </>
  );
}

/**
 * The action_config editor (E-SEC-UI.4), rendered differently per SAFE
 * action_type — never a control that could produce a third, unsafe type:
 *
 * - `http`: a single "Webhook URL" field, built into the exact `{"url": ...}`
 *   shape `policy.HTTPAction.Run` reads (internal/policy/actions.go:66-68) —
 *   the ONLY key that action consults. No headers field: the backend action
 *   implementation supports none.
 * - `notification`: a raw JSON textarea. `policy.NotificationAction` has no
 *   fixed config shape in this deployment (see
 *   securityResponseRuleActionConfigJsonError's own doc comment) — a
 *   documented, honest choice rather than a fabricated fixed-key form.
 */
export function ActionConfigEditor({
  actionType,
  value,
  onChange,
  disabled,
  error,
}: {
  actionType: 'http' | 'notification';
  value: string;
  onChange: (v: string) => void;
  disabled: boolean;
  error?: string;
}) {
  if (actionType === 'http') {
    return (
      <FormField
        label="Webhook URL"
        description="POSTed the security-incident event as JSON when this rule fires (policy.HTTPAction)."
        errorText={error}
      >
        <Input
          value={value}
          onChange={({ detail }) => onChange(detail.value)}
          disabled={disabled}
          ariaLabel="Webhook URL"
          placeholder="https://example.com/hooks/security"
        />
      </FormField>
    );
  }
  return (
    <FormField
      label="Notification config (JSON)"
      description={'Opaque JSON handed to the notification action when it runs, e.g. {"channel": "#security-alerts"}.'}
      errorText={error}
    >
      <Textarea
        value={value}
        onChange={({ detail }) => onChange(detail.value)}
        disabled={disabled}
        ariaLabel="Notification config"
        placeholder="{}"
        rows={4}
      />
    </FormField>
  );
}
