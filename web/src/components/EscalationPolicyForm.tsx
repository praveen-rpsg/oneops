import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import { escalationPolicyNameError } from '../escalation';

// The one field createEscalationPolicyRequest/patchEscalationPolicyRequest
// share, reused by the create modal (routes/EscalationBoardPage.tsx) and the
// edit modal (components/EscalationPolicyDetail.tsx — which additionally
// renders a status Select, since only PATCH can change it) — the identical
// shared-sub-form shape ADR-ACT-002/ADR-ACT-005 established for
// AlertRuleConfigFields/OnCallScheduleFields.

export interface EscalationPolicyFieldValues {
  name: string;
}

export function EscalationPolicyFields({
  values,
  onChange,
  disabled,
}: {
  values: EscalationPolicyFieldValues;
  onChange: (values: EscalationPolicyFieldValues) => void;
  disabled: boolean;
}) {
  const nameError = escalationPolicyNameError(values.name);

  return (
    <FormField label="Policy name" errorText={nameError}>
      <Input
        value={values.name}
        onChange={({ detail }) => onChange({ ...values, name: detail.value })}
        disabled={disabled}
        ariaLabel="Policy name"
        placeholder="Required"
      />
    </FormField>
  );
}
