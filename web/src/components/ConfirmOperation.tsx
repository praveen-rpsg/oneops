import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
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
  const failure = problem ? explainFailure(problem) : null;

  return (
    <Modal
      visible
      header={`${operation.label} ${artifact.artifact}?`}
      // Modal handles Escape/overlay dismissal itself; a mid-flight request must not be
      // abandoned silently, so a busy confirmation ignores the dismiss request.
      onDismiss={() => {
        if (!busy) onCancel();
      }}
      closeAriaLabel="Cancel"
      footer={
        <Box float="right">
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={onCancel} disabled={busy}>
              Cancel
            </Button>
            <Button
              variant="primary"
              onClick={onConfirm}
              loading={busy}
              disabled={failure !== null && !failure.retryable}
            >
              {busy ? `${operation.label}ing…` : operation.label}
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <SpaceBetween size="m">
        <Box>{operation.intent}</Box>
        <Box padding="s">
          <strong>Result:</strong> {operation.effect}
        </Box>

        {failure && (
          <div role="alert">
            <Alert type="error" header={failure.title}>
              <SpaceBetween size="xs">
                <Box>{failure.advice}</Box>
                {problem?.instance && (
                  <Box fontSize="body-s">
                    Request ID <code>{problem.instance}</code>
                  </Box>
                )}
              </SpaceBetween>
            </Alert>
          </div>
        )}
      </SpaceBetween>
    </Modal>
  );
}
