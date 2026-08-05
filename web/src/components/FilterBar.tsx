import Button from '@cloudscape-design/components/button';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Select from '@cloudscape-design/components/select';
import type { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Box from '@cloudscape-design/components/box';
import { AUTHORITIES, LIFECYCLES, ROLES } from '../api';
import type { EstateFilter } from '../api';

const humanise = (v: string) => v.replace(/_/g, ' ').replace(/^\w/, (c) => c.toUpperCase());

const ANY: SelectProps.Option = { label: 'Any', value: '' };
const toOptions = (values: readonly string[]): SelectProps.Option[] => [
  ANY,
  ...values.map((v) => ({ label: humanise(v), value: v })),
];

const ROLE_OPTIONS = toOptions(ROLES);
const LIFECYCLE_OPTIONS = toOptions(LIFECYCLES);
const AUTHORITY_OPTIONS = toOptions(AUTHORITIES);

interface Props {
  filter: EstateFilter;
  onChange: (next: EstateFilter) => void;
  onClear: () => void;
  resultCount: number;
  loading: boolean;
}

export function FilterBar({ filter, onChange, onClear, resultCount, loading }: Props) {
  const active = Boolean(filter.role || filter.lifecycle || filter.authority || filter.q);
  const selected = (options: SelectProps.Option[], value?: string) =>
    options.find((o) => o.value === (value ?? '')) ?? ANY;

  return (
    <form role="search" onSubmit={(e) => e.preventDefault()}>
      <SpaceBetween size="m">
        <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap', alignItems: 'flex-end' }}>
          <div style={{ flex: '1 1 260px' }}>
            <FormField label="Search">
              <Input
                type="search"
                placeholder="Artifact name or metadata"
                value={filter.q ?? ''}
                onChange={({ detail }) => onChange({ ...filter, q: detail.value, cursor: undefined })}
              />
            </FormField>
          </div>

          <div style={{ minWidth: 180 }}>
            <FormField label="Authority">
              <Select
                selectedOption={selected(AUTHORITY_OPTIONS, filter.authority)}
                options={AUTHORITY_OPTIONS}
                onChange={({ detail }) =>
                  onChange({
                    ...filter,
                    authority: (detail.selectedOption.value || '') as EstateFilter['authority'],
                    cursor: undefined,
                  })
                }
              />
            </FormField>
          </div>

          <div style={{ minWidth: 180 }}>
            <FormField label="Role">
              <Select
                selectedOption={selected(ROLE_OPTIONS, filter.role)}
                options={ROLE_OPTIONS}
                onChange={({ detail }) =>
                  onChange({
                    ...filter,
                    role: (detail.selectedOption.value || '') as EstateFilter['role'],
                    cursor: undefined,
                  })
                }
              />
            </FormField>
          </div>

          <div style={{ minWidth: 180 }}>
            <FormField label="Lifecycle">
              <Select
                selectedOption={selected(LIFECYCLE_OPTIONS, filter.lifecycle)}
                options={LIFECYCLE_OPTIONS}
                onChange={({ detail }) =>
                  onChange({
                    ...filter,
                    lifecycle: (detail.selectedOption.value || '') as EstateFilter['lifecycle'],
                    cursor: undefined,
                  })
                }
              />
            </FormField>
          </div>

          {active && (
            <Button variant="link" onClick={onClear}>
              Clear
            </Button>
          )}
        </div>

        <Box float="right" color="text-body-secondary" fontSize="body-s">
          <span aria-live="polite">{loading ? 'Loading…' : `${resultCount} shown`}</span>
        </Box>
      </SpaceBetween>
    </form>
  );
}
