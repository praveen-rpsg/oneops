import { useEffect, useRef } from 'react';
import type { ConfigObject, ProblemDetail } from '../api';
import type { Operation } from '../governance';
import { explainFailure } from '../governance';

interface Props {
  operation: Operation;
  artifact: ConfigObject;
  busy: boolean;
  problem: ProblemDetail | null;
  onConfirm: () => void;
  onCancel: () => void;
}

export function ConfirmOperation({
  operation,
  artifact,
  busy,
  problem,
  onConfirm,
  onCancel,
}: Props) {
  const confirmRef = useRef<HTMLButtonElement>(null);
  const failure = problem ? explainFailure(problem) : null;

  // Move focus into the dialog on open, and close on Escape.
  useEffect(() => {
    confirmRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !busy) onCancel();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [busy, onCancel]);

  return (
    <div className="scrim" onMouseDown={() => !busy && onCancel()}>
      <div
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby="confirm-title"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <h2 id="confirm-title">
          {operation.label} {artifact.artifact}?
        </h2>

        <p>{operation.intent}</p>

        <p className="effect">
          <strong>Result:</strong> {operation.effect}
        </p>

        {failure && (
          <div className="dialog-error" role="alert">
            <strong>{failure.title}</strong>
            <p>{failure.advice}</p>
            {problem?.instance && (
              <p className="request-id">
                Request ID <code>{problem.instance}</code>
              </p>
            )}
          </div>
        )}

        <div className="dialog-actions">
          <button type="button" className="btn" onClick={onCancel} disabled={busy}>
            Cancel
          </button>
          <button
            ref={confirmRef}
            type="button"
            className="btn btn-primary"
            onClick={onConfirm}
            disabled={busy || (failure !== null && !failure.retryable)}
          >
            {busy ? `${operation.label}ing…` : operation.label}
          </button>
        </div>
      </div>
    </div>
  );
}
