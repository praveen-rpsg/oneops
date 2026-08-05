import { useCallback, useEffect, useRef, useState } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import { ApiError, listArtifacts } from '../api';
import type { ConfigObject, EstateFilter, ProblemDetail } from '../api';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Header from '@cloudscape-design/components/header';
import SpaceBetween from '@cloudscape-design/components/space-between';
import { EstateTable } from '../components/EstateTable';
import { FilterBar } from '../components/FilterBar';
import { EmptyEstate, EmptyResults, ErrorState } from '../components/States';

const EMPTY: EstateFilter = {};

/** Filter state lives in the URL so a view can be shared in Slack. */
function readFilter(search: string): EstateFilter {
  const p = new URLSearchParams(search);
  return {
    role: (p.get('role') as EstateFilter['role']) ?? '',
    lifecycle: (p.get('lifecycle') as EstateFilter['lifecycle']) ?? '',
    authority: (p.get('authority') as EstateFilter['authority']) ?? '',
    q: p.get('q') ?? '',
  };
}

function writeFilter(f: EstateFilter) {
  const p = new URLSearchParams();
  if (f.role) p.set('role', f.role);
  if (f.lifecycle) p.set('lifecycle', f.lifecycle);
  if (f.authority) p.set('authority', f.authority);
  if (f.q) p.set('q', f.q);
  const qs = p.toString();
  window.history.replaceState(null, '', qs ? `?${qs}` : window.location.pathname);
}

export function EstatePage() {
  const navigate = useNavigate();
  const location = useLocation();
  const [filter, setFilter] = useState<EstateFilter>(() => readFilter(window.location.search));
  const [items, setItems] = useState<ConfigObject[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);
  const filtered = Boolean(filter.role || filter.lifecycle || filter.authority || filter.q);
  const debounce = useRef<number>();

  const load = useCallback((f: EstateFilter, signal: AbortSignal) => {
    setLoading(true);
    setProblem(null);
    listArtifacts(f, signal)
      .then((page) => {
        setItems(page.items ?? []);
        setCursor(page.next_cursor);
      })
      .catch((err: unknown) => {
        if (signal.aborted) return;
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not reach OneOps', status: 0, detail: String(err) },
        );
        setItems([]);
      })
      .finally(() => {
        if (!signal.aborted) setLoading(false);
      });
  }, []);

  // Browser back/forward must restore the filters that were active then.
  useEffect(() => {
    const sync = () => setFilter(readFilter(window.location.search));
    window.addEventListener('popstate', sync);
    return () => window.removeEventListener('popstate', sync);
  }, []);

  // Debounce only the free-text field; selects apply immediately.
  useEffect(() => {
    const ctrl = new AbortController();
    writeFilter(filter);
    window.clearTimeout(debounce.current);
    debounce.current = window.setTimeout(() => load(filter, ctrl.signal), filter.q ? 250 : 0);
    return () => {
      window.clearTimeout(debounce.current);
      ctrl.abort();
    };
  }, [filter, reloads, load]);

  const clear = () => setFilter(EMPTY);
  const retry = () => setReloads((n) => n + 1);

  const loadMore = () => {
    const ctrl = new AbortController();
    setLoading(true);
    listArtifacts({ ...filter, cursor }, ctrl.signal)
      .then((page) => {
        setItems((prev) => [...prev, ...(page.items ?? [])]);
        setCursor(page.next_cursor);
      })
      .catch((err: unknown) => {
        setProblem(
          err instanceof ApiError
            ? err.problem
            : { title: 'Could not load more', status: 0, detail: String(err) },
        );
      })
      .finally(() => setLoading(false));
  };

  const openArtifact = (id: string) => {
    navigate(`/artifacts/${encodeURIComponent(id)}`, {
      state: { from: `${location.pathname}${location.search}` },
    });
  };

  const empty = !problem
    ? filtered
      ? <EmptyResults onClear={clear} />
      : <EmptyEstate />
    : undefined;

  return (
    <SpaceBetween size="l">
      <Header variant="h1" description="Configuration objects under §8 governance.">
        Governed estate
      </Header>

      <FilterBar filter={filter} onChange={setFilter} onClear={clear} resultCount={items.length} loading={loading} />

      {problem && <ErrorState problem={problem} onRetry={retry} />}

      {!problem && (
        <EstateTable items={items} onOpen={openArtifact} loading={loading && items.length === 0} empty={empty} />
      )}

      {!problem && cursor && (
        <Box textAlign="center">
          <Button onClick={loadMore} loading={loading && items.length > 0}>
            Load more
          </Button>
        </Box>
      )}
    </SpaceBetween>
  );
}
