// Shared state primitives. Every screen reuses these — do not inline replacements.
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import SpaceBetween from '@cloudscape-design/components/space-between';
import type { ProblemDetail } from '../api';

/** No artifacts exist at all — distinct from a filter matching nothing. */
export function EmptyEstate() {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No artifacts yet</b>
        <Box variant="p" color="text-body-secondary">
          Nothing has been registered in this estate. Artifacts appear here once they are
          created through the API.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

/** Filters matched nothing — offer the way out. */
export function EmptyResults({ onClear }: { onClear: () => void }) {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No artifacts match these filters</b>
        <Box variant="p" color="text-body-secondary">
          Try widening the search, or clear the filters to see the whole estate.
        </Box>
        <Button onClick={onClear}>Clear filters</Button>
      </SpaceBetween>
    </Box>
  );
}

/**
 * Renders the server's RFC 7807 detail verbatim — its messages cite the governing rule.
 * `role="alert"` on the wrapper (Cloudscape's `Alert` does not set it itself) so the
 * failure is announced and reliably queryable in tests.
 */
export function ErrorState({
  problem,
  onRetry,
}: {
  problem: ProblemDetail;
  onRetry: () => void;
}) {
  return (
    <div role="alert">
      <Alert
        type="error"
        header={problem.title}
        action={<Button onClick={onRetry}>Try again</Button>}
      >
        <SpaceBetween size="xs">
          {problem.detail && <Box>{problem.detail}</Box>}
          {problem.instance && (
            <Box fontSize="body-s" color="text-body-secondary">
              Request ID <code>{problem.instance}</code>
            </Box>
          )}
        </SpaceBetween>
      </Alert>
    </div>
  );
}
