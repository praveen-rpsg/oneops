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

/**
 * Authority is the primary dimension: it answers "does this govern now?".
 * Lifecycle, Retention and Role are secondary and rendered as plain text.
 * Never colour alone — the label always carries the meaning (WCAG 1.4.1).
 */
export function AuthorityPill({ value }: { value: Authority }) {
  return (
    <span className={`pill pill-${value}`} title={TITLE[value]}>
      {LABEL[value]}
    </span>
  );
}
