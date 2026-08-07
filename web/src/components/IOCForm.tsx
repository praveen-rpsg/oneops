import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Select from '@cloudscape-design/components/select';
import type { SelectProps } from '@cloudscape-design/components/select';
import Textarea from '@cloudscape-design/components/textarea';
import Toggle from '@cloudscape-design/components/toggle';
import { INCIDENT_SEVERITIES } from '../iocs';
import type { IncidentSeverity } from '../iocs';
import { humanise } from '../incidentPresentation';

// The four fields `patchIOCRequest` accepts (E-SEC-UI.2), shared by the
// create modal (routes/IndicatorsPage.tsx — which additionally collects
// indicator_type/indicator_value, fixed at creation) and the edit modal
// (components/IOCDetail.tsx — which does not, since domain.IOCPatch cannot
// touch them). severity renders as a Select constrained to the real backend
// value set from iocs.ts, so a bad enum is impossible to submit through this
// form.

const SEVERITY_OPTIONS: SelectProps.Option[] = INCIDENT_SEVERITIES.map((s) => ({ value: s, label: humanise(s) }));

export interface IOCConfigValues {
  severity: IncidentSeverity;
  enabled: boolean;
  description: string;
  source: string;
}

export interface IOCConfigErrors {
  description?: string;
  source?: string;
}

export function IOCConfigFields({
  values,
  onChange,
  errors,
  disabled,
}: {
  values: IOCConfigValues;
  onChange: (values: IOCConfigValues) => void;
  errors: IOCConfigErrors;
  disabled: boolean;
}) {
  return (
    <>
      <FormField label="Severity" description="The severity of the incident raised on a match">
        <Select
          selectedOption={SEVERITY_OPTIONS.find((o) => o.value === values.severity) ?? SEVERITY_OPTIONS[0]}
          onChange={({ detail }) => onChange({ ...values, severity: (detail.selectedOption.value ?? 'low') as IncidentSeverity })}
          options={SEVERITY_OPTIONS}
          disabled={disabled}
          ariaLabel="Severity"
        />
      </FormField>
      <FormField label="Enabled">
        <Toggle checked={values.enabled} onChange={({ detail }) => onChange({ ...values, enabled: detail.checked })} disabled={disabled}>
          {values.enabled ? 'Enabled' : 'Disabled'}
        </Toggle>
      </FormField>
      <FormField label="Description" description="Optional — why this indicator is on the watchlist" errorText={errors.description}>
        <Textarea
          value={values.description}
          onChange={({ detail }) => onChange({ ...values, description: detail.value })}
          disabled={disabled}
          ariaLabel="Description"
          placeholder="Optional"
        />
      </FormField>
      <FormField label="Source" description="Optional — where this indicator came from, e.g. misp" errorText={errors.source}>
        <Input
          value={values.source}
          onChange={({ detail }) => onChange({ ...values, source: detail.value })}
          disabled={disabled}
          ariaLabel="Source"
          placeholder="Optional"
        />
      </FormField>
    </>
  );
}
