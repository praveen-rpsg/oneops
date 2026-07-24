import type { ConfigObject, ProblemDetail } from './api';

/**
 * §8 operations the console can perform, with the preconditions verified in
 * internal/governance/transition.go. An operation is offered only when its
 * precondition holds — an action that cannot succeed is worse than no action.
 */
export interface Operation {
  id: 'ratify';
  label: string;
  /** What the user is about to do, in their words. */
  intent: string;
  /** The resulting state, stated before they commit. */
  effect: string;
  available: (o: ConfigObject) => boolean;
  /** Why it is unavailable, shown instead of a disabled button with no reason. */
  unavailableBecause: (o: ConfigObject) => string;
}

export const RATIFY: Operation = {
  id: 'ratify',
  label: 'Ratify',
  intent: 'Ratifying makes this artifact govern. It becomes part of the current baseline.',
  effect: 'Lifecycle → Ratified · Authority → Active · Retention → Current baseline',
  // transition.go: requires draft or in_review.
  available: (o) => o.lifecycle === 'draft' || o.lifecycle === 'in_review',
  unavailableBecause: (o) =>
    `Ratification requires a draft or in-review artifact. This one is ${o.lifecycle.replace(/_/g, ' ')}.`,
};

/**
 * Turns a transport failure into something a governance user can act on.
 * Every status the handler can produce is covered explicitly; the server's own
 * RFC 7807 `detail` is preferred wherever it is more specific than ours.
 */
export function explainFailure(p: ProblemDetail): { title: string; advice: string; retryable: boolean } {
  switch (p.status) {
    case 400:
      return {
        title: 'The request was rejected',
        advice: p.detail ?? 'The operation was malformed.',
        retryable: false,
      };
    case 401:
      return {
        title: 'Your session has expired',
        advice: 'Sign in again to continue.',
        retryable: false,
      };
    case 403:
      return {
        title: 'You do not have permission',
        advice: 'Performing this operation requires editor access. Ask an administrator.',
        retryable: false,
      };
    case 404:
      return {
        title: 'This artifact no longer exists',
        advice: 'It may have been deleted. Return to the estate and refresh.',
        retryable: false,
      };
    case 409:
      return {
        title: 'The operation is not permitted',
        // The engine's message names the exact constitutional reason.
        advice: p.detail ?? 'The artifact is not in a state that allows this operation.',
        retryable: false,
      };
    case 412:
      return {
        title: 'This artifact changed while you were viewing it',
        advice: 'Someone else modified it. Refresh to see the current state, then try again.',
        retryable: true,
      };
    case 422:
      return {
        title: 'Operation not supported',
        advice: p.detail ?? 'This operation is not implemented on this deployment.',
        retryable: false,
      };
    case 428:
      return {
        title: 'A precondition was missing',
        advice: p.detail ?? 'The request needs a version check. Refresh and try again.',
        retryable: true,
      };
    default:
      return {
        title: p.status >= 500 ? 'OneOps could not complete the operation' : 'The operation failed',
        advice: p.detail ?? 'Try again. If it keeps failing, contact support with the request id.',
        retryable: p.status >= 500 || p.status === 0,
      };
  }
}
