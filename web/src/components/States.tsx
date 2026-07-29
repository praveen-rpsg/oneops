// Shared state primitives. Every screen reuses these — do not inline replacements.
import type { ProblemDetail } from '../api';

export function SkeletonRows({ rows = 8 }: { rows?: number }) {
  return (
    <tbody aria-busy="true" aria-label="Loading artifacts">
      {Array.from({ length: rows }).map((_, i) => (
        <tr key={i} className="skeleton-row">
          {Array.from({ length: 5 }).map((__, j) => (
            <td key={j}>
              <span className="skeleton" />
            </td>
          ))}
        </tr>
      ))}
    </tbody>
  );
}

/** No artifacts exist at all — distinct from a filter matching nothing. */
export function EmptyEstate() {
  return (
    <div className="state" role="status">
      <h2>No artifacts yet</h2>
      <p>
        Nothing has been registered in this estate. Artifacts appear here once they are
        created through the API.
      </p>
    </div>
  );
}

/** Filters matched nothing — offer the way out. */
export function EmptyResults({ onClear }: { onClear: () => void }) {
  return (
    <div className="state" role="status">
      <h2>No artifacts match these filters</h2>
      <p>Try widening the search, or clear the filters to see the whole estate.</p>
      <button type="button" className="btn" onClick={onClear}>
        Clear filters
      </button>
    </div>
  );
}

/** Renders the server's RFC 7807 detail verbatim — its messages cite the governing rule. */
export function ErrorState({
  problem,
  onRetry,
}: {
  problem: ProblemDetail;
  onRetry: () => void;
}) {
  return (
    <div className="state state-error" role="alert">
      <h2>{problem.title}</h2>
      {problem.detail && <p>{problem.detail}</p>}
      <button type="button" className="btn" onClick={onRetry}>
        Try again
      </button>
      {problem.instance && (
        <p className="request-id">
          Request ID <code>{problem.instance}</code>
        </p>
      )}
    </div>
  );
}
