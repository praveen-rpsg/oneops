import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Select from '@cloudscape-design/components/select';
import type { SelectProps } from '@cloudscape-design/components/select';
import Textarea from '@cloudscape-design/components/textarea';
import { humanise } from '../incidentPresentation';
import { riskCategoryError, RISK_IMPACTS, RISK_LIKELIHOODS, RISK_TREATMENTS } from '../risks';
import type { RiskImpact, RiskLikelihood, RiskTreatment } from '../risks';

// The fields `patchRiskRequest` accepts alongside title (E-SEC-UI.3), shared
// by the create modal (routes/RiskRegisterPage.tsx) and the edit modal
// (components/RiskDetail.tsx) — mirrors SecurityRuleConfigFields' shared
// "one field set, two callers" shape. Every closed-enum field renders as a
// Select constrained to the real backend value set from risks.ts, so a bad
// enum is impossible to submit through this form; category/asset_id stay
// free-text `Input`s since both are open-but-bounded, not closed enums.

const LIKELIHOOD_OPTIONS: SelectProps.Option[] = RISK_LIKELIHOODS.map((l) => ({ value: l, label: humanise(l) }));
const IMPACT_OPTIONS: SelectProps.Option[] = RISK_IMPACTS.map((i) => ({ value: i, label: humanise(i) }));
const TREATMENT_OPTIONS: SelectProps.Option[] = [
  { value: '', label: 'Not yet decided' },
  ...RISK_TREATMENTS.map((t) => ({ value: t, label: humanise(t) })),
];

export interface RiskFormValues {
  title: string;
  description: string;
  category: string;
  likelihood: RiskLikelihood;
  impact: RiskImpact;
  /** '' means "not yet decided" — clears domain.Risk.Treatment. */
  treatment: RiskTreatment | '';
  /** '' means "not linked to a Configuration Item" — clears domain.Risk.AssetID. */
  assetId: string;
}

export function RiskFormFields({
  values,
  onChange,
  disabled,
  titleError,
}: {
  values: RiskFormValues;
  onChange: (values: RiskFormValues) => void;
  disabled: boolean;
  titleError?: string;
}) {
  const categoryError = riskCategoryError(values.category);
  return (
    <>
      <FormField label="Title" errorText={titleError}>
        <Input
          value={values.title}
          onChange={({ detail }) => onChange({ ...values, title: detail.value })}
          disabled={disabled}
          ariaLabel="Title"
          placeholder="Required"
        />
      </FormField>
      <FormField label="Description" description="Optional — context, evidence, rationale">
        <Textarea
          value={values.description}
          onChange={({ detail }) => onChange({ ...values, description: detail.value })}
          disabled={disabled}
          ariaLabel="Description"
        />
      </FormField>
      <FormField
        label="Category"
        description="Optional, lower-case snake_case, e.g. operational, compliance, security"
        errorText={categoryError}
      >
        <Input
          value={values.category}
          onChange={({ detail }) => onChange({ ...values, category: detail.value })}
          disabled={disabled}
          ariaLabel="Category"
        />
      </FormField>
      <FormField label="Likelihood">
        <Select
          selectedOption={LIKELIHOOD_OPTIONS.find((o) => o.value === values.likelihood) ?? LIKELIHOOD_OPTIONS[0]}
          onChange={({ detail }) => onChange({ ...values, likelihood: (detail.selectedOption.value ?? 'possible') as RiskLikelihood })}
          options={LIKELIHOOD_OPTIONS}
          disabled={disabled}
          ariaLabel="Likelihood"
        />
      </FormField>
      <FormField label="Impact">
        <Select
          selectedOption={IMPACT_OPTIONS.find((o) => o.value === values.impact) ?? IMPACT_OPTIONS[0]}
          onChange={({ detail }) => onChange({ ...values, impact: (detail.selectedOption.value ?? 'moderate') as RiskImpact })}
          options={IMPACT_OPTIONS}
          disabled={disabled}
          ariaLabel="Impact"
        />
      </FormField>
      <FormField label="Treatment" description="The operator's disposition decision — optional">
        <Select
          selectedOption={TREATMENT_OPTIONS.find((o) => o.value === values.treatment) ?? TREATMENT_OPTIONS[0]}
          onChange={({ detail }) => onChange({ ...values, treatment: (detail.selectedOption.value ?? '') as RiskTreatment | '' })}
          options={TREATMENT_OPTIONS}
          disabled={disabled}
          ariaLabel="Treatment"
        />
      </FormField>
      <FormField label="Asset ID" description="Optional — links this risk to a Configuration Item">
        <Input
          value={values.assetId}
          onChange={({ detail }) => onChange({ ...values, assetId: detail.value })}
          disabled={disabled}
          ariaLabel="Asset ID"
        />
      </FormField>
    </>
  );
}
