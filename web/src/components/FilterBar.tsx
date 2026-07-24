import { AUTHORITIES, LIFECYCLES, ROLES } from '../api';
import type { EstateFilter } from '../api';

const humanise = (v: string) => v.replace(/_/g, ' ').replace(/^\w/, (c) => c.toUpperCase());

interface Props {
  filter: EstateFilter;
  onChange: (next: EstateFilter) => void;
  onClear: () => void;
  resultCount: number;
  loading: boolean;
}

export function FilterBar({ filter, onChange, onClear, resultCount, loading }: Props) {
  const active = Boolean(filter.role || filter.lifecycle || filter.authority || filter.q);

  return (
    <form className="filterbar" role="search" onSubmit={(e) => e.preventDefault()}>
      <div className="field field-grow">
        <label htmlFor="f-q">Search</label>
        <input
          id="f-q"
          type="search"
          placeholder="Artifact name or metadata"
          value={filter.q ?? ''}
          onChange={(e) => onChange({ ...filter, q: e.target.value, cursor: undefined })}
        />
      </div>

      <div className="field">
        <label htmlFor="f-authority">Authority</label>
        <select
          id="f-authority"
          value={filter.authority ?? ''}
          onChange={(e) =>
            onChange({ ...filter, authority: e.target.value as never, cursor: undefined })
          }
        >
          <option value="">Any</option>
          {AUTHORITIES.map((a) => (
            <option key={a} value={a}>
              {humanise(a)}
            </option>
          ))}
        </select>
      </div>

      <div className="field">
        <label htmlFor="f-role">Role</label>
        <select
          id="f-role"
          value={filter.role ?? ''}
          onChange={(e) =>
            onChange({ ...filter, role: e.target.value as never, cursor: undefined })
          }
        >
          <option value="">Any</option>
          {ROLES.map((r) => (
            <option key={r} value={r}>
              {humanise(r)}
            </option>
          ))}
        </select>
      </div>

      <div className="field">
        <label htmlFor="f-lifecycle">Lifecycle</label>
        <select
          id="f-lifecycle"
          value={filter.lifecycle ?? ''}
          onChange={(e) =>
            onChange({ ...filter, lifecycle: e.target.value as never, cursor: undefined })
          }
        >
          <option value="">Any</option>
          {LIFECYCLES.map((l) => (
            <option key={l} value={l}>
              {humanise(l)}
            </option>
          ))}
        </select>
      </div>

      {active && (
        <button type="button" className="btn btn-quiet" onClick={onClear}>
          Clear
        </button>
      )}

      <p className="result-count" aria-live="polite">
        {loading ? 'Loading…' : `${resultCount} shown`}
      </p>
    </form>
  );
}
