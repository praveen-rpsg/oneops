import DateInput from '@cloudscape-design/components/date-input';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Select from '@cloudscape-design/components/select';
import type { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import TimeInput from '@cloudscape-design/components/time-input';
import { ON_CALL_HANDOFF_PRESETS, onCallHandoffIntervalError, onCallScheduleNameError } from '../onCall';

// The three fields createOnCallScheduleRequest/patchOnCallScheduleRequest
// share (ADR-ACT-005), reused by the create modal (routes/OnCallBoardPage.tsx)
// and the edit modal (components/OnCallScheduleDetail.tsx — which additionally
// renders a status Select, since only PATCH can change it).

const HANDOFF_OPTIONS: SelectProps.Option[] = ON_CALL_HANDOFF_PRESETS.map((p) => ({ value: p.value, label: p.label }));

export interface OnCallScheduleFieldValues {
  name: string;
  handoffPreset: string;
  handoffCustomSeconds: string;
  rotationStartDate: string;
  rotationStartTime: string;
}

/** The interval this set of values actually names, resolving the preset to seconds or parsing the custom field. undefined if unanswered/invalid. */
export function resolveHandoffIntervalSeconds(values: OnCallScheduleFieldValues): number | undefined {
  const preset = ON_CALL_HANDOFF_PRESETS.find((p) => p.value === values.handoffPreset);
  if (preset?.seconds !== undefined) return preset.seconds;
  const n = Number(values.handoffCustomSeconds);
  return values.handoffCustomSeconds.trim() !== '' && Number.isFinite(n) ? n : undefined;
}

export function OnCallScheduleFields({
  values,
  onChange,
  disabled,
}: {
  values: OnCallScheduleFieldValues;
  onChange: (values: OnCallScheduleFieldValues) => void;
  disabled: boolean;
}) {
  const nameError = onCallScheduleNameError(values.name);
  const resolvedSeconds = resolveHandoffIntervalSeconds(values);
  const handoffError =
    values.handoffPreset === 'custom' && values.handoffCustomSeconds.trim() === ''
      ? undefined
      : resolvedSeconds === undefined
        ? undefined
        : onCallHandoffIntervalError(resolvedSeconds);

  return (
    <>
      <FormField label="Schedule name" errorText={nameError}>
        <Input
          value={values.name}
          onChange={({ detail }) => onChange({ ...values, name: detail.value })}
          disabled={disabled}
          ariaLabel="Schedule name"
          placeholder="Required"
        />
      </FormField>
      <FormField
        label="Handoff interval"
        description="How often the rotation hands off to the next participant."
        errorText={handoffError}
      >
        <SpaceBetween direction="horizontal" size="xs">
          <Select
            selectedOption={HANDOFF_OPTIONS.find((o) => o.value === values.handoffPreset) ?? HANDOFF_OPTIONS[0]}
            onChange={({ detail }) => onChange({ ...values, handoffPreset: detail.selectedOption.value ?? 'custom' })}
            options={HANDOFF_OPTIONS}
            disabled={disabled}
            ariaLabel="Handoff interval preset"
          />
          {values.handoffPreset === 'custom' && (
            <Input
              type="number"
              value={values.handoffCustomSeconds}
              onChange={({ detail }) => onChange({ ...values, handoffCustomSeconds: detail.value })}
              disabled={disabled}
              ariaLabel="Handoff interval seconds"
              placeholder="Seconds"
            />
          )}
        </SpaceBetween>
      </FormField>
      <FormField label="Rotation start" description="The instant the rotation's first handoff is anchored to (your local time zone).">
        <SpaceBetween direction="horizontal" size="xs">
          <DateInput
            value={values.rotationStartDate}
            onChange={({ detail }) => onChange({ ...values, rotationStartDate: detail.value })}
            disabled={disabled}
            ariaLabel="Rotation start date"
            placeholder="YYYY/MM/DD"
          />
          <TimeInput
            value={values.rotationStartTime}
            onChange={({ detail }) => onChange({ ...values, rotationStartTime: detail.value })}
            disabled={disabled}
            format="hh:mm"
            use24Hour
            ariaLabel="Rotation start time"
            placeholder="hh:mm"
          />
        </SpaceBetween>
      </FormField>
    </>
  );
}
