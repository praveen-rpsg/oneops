import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Select from '@cloudscape-design/components/select';
import type { SelectProps } from '@cloudscape-design/components/select';
import Toggle from '@cloudscape-design/components/toggle';
import { INCIDENT_SEVERITIES, OBSERVATION_SEVERITIES } from '../securityRules';
import type { IncidentSeverity, ObservationSeverity } from '../securityRules';
import { humanise } from '../incidentPresentation';

// The five fields `patchSecurityRuleRequest` accepts (E-SEC-UI.2), shared by
// the create modal (routes/DetectionRulesPage.tsx — which additionally
// collects asset_id/observation_type, fixed at creation) and the edit modal
// (components/SecurityRuleDetail.tsx — which does not, since
// domain.SecurityRulePatch cannot touch them). Every enum field renders as a
// Select constrained to the real backend value set from securityRules.ts, so
// a bad enum is impossible to submit through this form.

const MIN_SEVERITY_OPTIONS: SelectProps.Option[] = OBSERVATION_SEVERITIES.map((s) => ({ value: s, label: humanise(s) }));
const INCIDENT_SEVERITY_OPTIONS: SelectProps.Option[] = INCIDENT_SEVERITIES.map((s) => ({ value: s, label: humanise(s) }));

export interface SecurityRuleConfigValues {
  minSeverity: ObservationSeverity;
  windowSeconds: string;
  thresholdCount: string;
  incidentSeverity: IncidentSeverity;
  enabled: boolean;
}

export interface SecurityRuleConfigErrors {
  windowSeconds?: string;
  thresholdCount?: string;
}

export function SecurityRuleConfigFields({
  values,
  onChange,
  errors,
  disabled,
}: {
  values: SecurityRuleConfigValues;
  onChange: (values: SecurityRuleConfigValues) => void;
  errors: SecurityRuleConfigErrors;
  disabled: boolean;
}) {
  return (
    <>
      <FormField label="Minimum severity" description="Count only observations at or above this severity">
        <Select
          selectedOption={MIN_SEVERITY_OPTIONS.find((o) => o.value === values.minSeverity) ?? MIN_SEVERITY_OPTIONS[0]}
          onChange={({ detail }) =>
            onChange({ ...values, minSeverity: (detail.selectedOption.value ?? 'info') as ObservationSeverity })
          }
          options={MIN_SEVERITY_OPTIONS}
          disabled={disabled}
          ariaLabel="Minimum severity"
        />
      </FormField>
      <FormField
        label="Window (seconds)"
        description="The trailing lookback the count is taken over"
        errorText={errors.windowSeconds}
      >
        <Input
          type="number"
          value={values.windowSeconds}
          onChange={({ detail }) => onChange({ ...values, windowSeconds: detail.value })}
          disabled={disabled}
          ariaLabel="Window seconds"
          placeholder="Required"
        />
      </FormField>
      <FormField
        label="Threshold count"
        description="Fire when the matching count meets or exceeds this value"
        errorText={errors.thresholdCount}
      >
        <Input
          type="number"
          value={values.thresholdCount}
          onChange={({ detail }) => onChange({ ...values, thresholdCount: detail.value })}
          disabled={disabled}
          ariaLabel="Threshold count"
          placeholder="Required"
        />
      </FormField>
      <FormField label="Incident severity" description="The severity of the incident raised on a firing">
        <Select
          selectedOption={
            INCIDENT_SEVERITY_OPTIONS.find((o) => o.value === values.incidentSeverity) ?? INCIDENT_SEVERITY_OPTIONS[0]
          }
          onChange={({ detail }) =>
            onChange({ ...values, incidentSeverity: (detail.selectedOption.value ?? 'low') as IncidentSeverity })
          }
          options={INCIDENT_SEVERITY_OPTIONS}
          disabled={disabled}
          ariaLabel="Incident severity"
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
