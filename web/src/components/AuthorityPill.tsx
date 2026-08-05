import StatusIndicator from '@cloudscape-design/components/status-indicator';
import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import type { Authority } from '../api';

const LABEL: Record<Authority, string> = {
  active: 'Active',
  historical: 'Historical',
  non_normative: 'Non-normative',
};

const TITLE: Record<Authority, string> = {
  active: 'Currently governs decisions',
  historical: 'Governed once; superseded or retired',
  non_normative: 'Carries no governing force by design',
};

const TYPE: Record<Authority, StatusIndicatorProps.Type> = {
  active: 'success',
  historical: 'stopped',
  non_normative: 'info',
};

/**
 * Authority is the primary dimension: it answers "does this govern now?".
 * Lifecycle, Retention and Role are secondary and rendered as plain text.
 * Never colour alone — the label always carries the meaning (WCAG 1.4.1),
 * which `StatusIndicator` guarantees by pairing every colour with an icon and text.
 */
export function AuthorityPill({ value }: { value: Authority }) {
  return (
    <StatusIndicator type={TYPE[value]} nativeAttributes={{ title: TITLE[value] }}>
      {LABEL[value]}
    </StatusIndicator>
  );
}
