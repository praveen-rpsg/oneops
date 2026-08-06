import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Select from '@cloudscape-design/components/select';
import type { SelectProps } from '@cloudscape-design/components/select';
import Toggle from '@cloudscape-design/components/toggle';
import { ALERT_COMPARATORS, ALERT_SEVERITIES, ALERT_SYMPTOM_CLASSES } from '../alertRules';
import type { AlertComparator, AlertSeverity, AlertSymptomClass } from '../alertRules';
import { COMPARATOR_SYMBOL } from '../alertPresentation';
import { humanise } from '../incidentPresentation';

// The seven fields `patchAlertRuleRequest` accepts (ADR-ACT-002), shared by
// the create modal (routes/AlertsBoardPage.tsx — which additionally collects
// asset_id/metric, fixed at creation) and the edit modal
// (components/AlertRuleDetail.tsx — which does not, since
// domain.AlertRulePatch cannot touch them). Every enum field renders as a
// Select constrained to the real backend value set from alertRules.ts, so a
// bad enum is impossible to submit through this form.

const COMPARATOR_OPTIONS: SelectProps.Option[] = ALERT_COMPARATORS.map((c) => ({
  value: c,
  label: `${c} (${COMPARATOR_SYMBOL[c]})`,
}));
const SEVERITY_OPTIONS: SelectProps.Option[] = ALERT_SEVERITIES.map((s) => ({ value: s, label: humanise(s) }));
const SYMPTOM_CLASS_OPTIONS: SelectProps.Option[] = ALERT_SYMPTOM_CLASSES.map((c) => ({ value: c, label: humanise(c) }));

export interface AlertRuleConfigValues {
  comparator: AlertComparator;
  threshold: string;
  forDurationSeconds: string;
  severity: AlertSeverity;
  symptomClass: AlertSymptomClass;
  flapDwellSeconds: string;
  enabled: boolean;
}

export interface AlertRuleConfigErrors {
  threshold?: string;
  forDurationSeconds?: string;
  flapDwellSeconds?: string;
}

export function AlertRuleConfigFields({
  values,
  onChange,
  errors,
  disabled,
}: {
  values: AlertRuleConfigValues;
  onChange: (values: AlertRuleConfigValues) => void;
  errors: AlertRuleConfigErrors;
  disabled: boolean;
}) {
  return (
    <>
      <FormField label="Comparator">
        <Select
          selectedOption={COMPARATOR_OPTIONS.find((o) => o.value === values.comparator) ?? COMPARATOR_OPTIONS[0]}
          onChange={({ detail }) =>
            onChange({ ...values, comparator: (detail.selectedOption.value ?? 'gt') as AlertComparator })
          }
          options={COMPARATOR_OPTIONS}
          disabled={disabled}
          ariaLabel="Comparator"
        />
      </FormField>
      <FormField label="Threshold" errorText={errors.threshold}>
        <Input
          type="number"
          value={values.threshold}
          onChange={({ detail }) => onChange({ ...values, threshold: detail.value })}
          disabled={disabled}
          ariaLabel="Threshold"
          placeholder="Required"
        />
      </FormField>
      <FormField
        label="For duration (seconds)"
        description="How long the breach must be sustained before the rule fires"
        errorText={errors.forDurationSeconds}
      >
        <Input
          type="number"
          value={values.forDurationSeconds}
          onChange={({ detail }) => onChange({ ...values, forDurationSeconds: detail.value })}
          disabled={disabled}
          ariaLabel="For duration seconds"
          placeholder="Required"
        />
      </FormField>
      <FormField label="Severity">
        <Select
          selectedOption={SEVERITY_OPTIONS.find((o) => o.value === values.severity) ?? SEVERITY_OPTIONS[0]}
          onChange={({ detail }) =>
            onChange({ ...values, severity: (detail.selectedOption.value ?? 'warning') as AlertSeverity })
          }
          options={SEVERITY_OPTIONS}
          disabled={disabled}
          ariaLabel="Severity"
        />
      </FormField>
      <FormField label="Symptom class">
        <Select
          selectedOption={SYMPTOM_CLASS_OPTIONS.find((o) => o.value === values.symptomClass) ?? SYMPTOM_CLASS_OPTIONS[0]}
          onChange={({ detail }) =>
            onChange({ ...values, symptomClass: (detail.selectedOption.value ?? 'unspecified') as AlertSymptomClass })
          }
          options={SYMPTOM_CLASS_OPTIONS}
          disabled={disabled}
          ariaLabel="Symptom class"
        />
      </FormField>
      <FormField
        label="Flap dwell (seconds)"
        description="Optional — how long a candidate state must be stable before it commits"
        errorText={errors.flapDwellSeconds}
      >
        <Input
          type="number"
          value={values.flapDwellSeconds}
          onChange={({ detail }) => onChange({ ...values, flapDwellSeconds: detail.value })}
          disabled={disabled}
          ariaLabel="Flap dwell seconds"
          placeholder="Optional"
        />
      </FormField>
      <FormField label="Enabled">
        <Toggle
          checked={values.enabled}
          onChange={({ detail }) => onChange({ ...values, enabled: detail.checked })}
          disabled={disabled}
        >
          {values.enabled ? 'Enabled' : 'Disabled'}
        </Toggle>
      </FormField>
    </>
  );
}
