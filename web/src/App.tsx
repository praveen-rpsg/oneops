import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError, listArtifacts } from './api';
import type { ConfigObject, EstateFilter, ProblemDetail } from './api';
import { completeSignIn, fetchAuthConfig, getSubject, getToken, signIn, signOut } from './auth';
import type { AuthConfig } from './auth';
import { ArtifactDetail } from './components/ArtifactDetail';
import { EstateTable } from './components/EstateTable';
import { FilterBar } from './components/FilterBar';
import { SignIn } from './components/SignIn';
import { EmptyEstate, EmptyResults, ErrorState, SkeletonRows } from './components/States';

type AuthState =
  | { phase: 'checking' }
  | { phase: 'signed-out'; cfg: AuthConfig; error?: string }
  | { phase: 'redirecting'; cfg: AuthConfig }
  | { phase: 'ready' };

const EMPTY: EstateFilter = {};

/**
 * Navigation is a query parameter, not a client-side router: `?artifact=<cfg_id>`
 * opens the detail view. One screen deep needs no routing library, keeps the URL
 * shareable, and leaves the server's 404 contract untouched.
 */
const readSelected = () => new URLSearchParams(window.location.search).get('artifact');

/** Filter state lives in the URL so a view can be shared in Slack. */
function readFilter(): EstateFilter {
  const p = new URLSearchParams(window.location.search);
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

export default function App() {
  const [auth, setAuth] = useState<AuthState>({ phase: 'checking' });
  const [selected, setSelected] = useState<string | null>(readSelected);
  const [filter, setFilter] = useState<EstateFilter>(readFilter);
  const [items, setItems] = useState<ConfigObject[]>([]);
  const [cursor, setCursor] = useState<string | undefined>();
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);
  const filtered = Boolean(filter.role || filter.lifecycle || filter.authority || filter.q);
  const debounce = useRef<number>();

  const load = useCallback(
    (f: EstateFilter, signal: AbortSignal) => {
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
    },
    [],
  );

  // Establish the session before any data request is made. When the deployment
  // runs with auth disabled (local development) the console proceeds directly.
  useEffect(() => {
    let cancelled = false;

    (async () => {
      try {
        const cfg = await fetchAuthConfig();
        if (cancelled) return;

        if (!cfg.auth_enabled) {
          setAuth({ phase: 'ready' });
          return;
        }
        if (await completeSignIn(cfg)) {
          if (!cancelled) {
            setSelected(readSelected());
            setFilter(readFilter());
            setAuth({ phase: 'ready' });
          }
          return;
        }
        if (!cancelled) setAuth({ phase: 'signed-out', cfg });
      } catch (err) {
        if (!cancelled) {
          setAuth({
            phase: 'signed-out',
            cfg: { auth_enabled: true },
            error: err instanceof Error ? err.message : String(err),
          });
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, []);

  const beginSignIn = useCallback(async () => {
    if (auth.phase !== 'signed-out') return;
    setAuth({ phase: 'redirecting', cfg: auth.cfg });
    try {
      await signIn(auth.cfg);
    } catch (err) {
      setAuth({
        phase: 'signed-out',
        cfg: auth.cfg,
        error: err instanceof Error ? err.message : String(err),
      });
    }
  }, [auth]);

  // Browser back/forward must move between estate and detail.
  useEffect(() => {
    const sync = () => {
      setSelected(readSelected());
      setFilter(readFilter());
    };
    window.addEventListener('popstate', sync);
    return () => window.removeEventListener('popstate', sync);
  }, []);

  const openArtifact = useCallback((id: string) => {
    const p = new URLSearchParams(window.location.search);
    p.set('artifact', id);
    window.history.pushState(null, '', `?${p.toString()}`);
    setSelected(id);
    window.scrollTo(0, 0);
  }, []);

  const closeArtifact = useCallback(() => {
    const p = new URLSearchParams(window.location.search);
    p.delete('artifact');
    const qs = p.toString();
    window.history.pushState(null, '', qs ? `?${qs}` : window.location.pathname);
    setSelected(null);
  }, []);

  // Debounce only the free-text field; selects apply immediately. Skipped while a
  // detail view is open — the list is not visible and its filters have not changed.
  useEffect(() => {
    if (selected || auth.phase !== 'ready') return;
    const ctrl = new AbortController();
    writeFilter(filter);
    window.clearTimeout(debounce.current);
    debounce.current = window.setTimeout(() => load(filter, ctrl.signal), filter.q ? 250 : 0);
    return () => {
      window.clearTimeout(debounce.current);
      ctrl.abort();
    };
  }, [filter, reloads, load, selected, auth.phase]);

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

  const showSkeleton = loading && items.length === 0 && !problem;

  const topbar = (
    <header className="topbar">
      <h1>OneOps</h1>
      <p className="subtitle">Governed estate</p>
      {getToken() && (
        <div className="session">
          <span className="who">{getSubject()}</span>
          <button type="button" className="link" onClick={signOut}>
            Sign out
          </button>
        </div>
      )}
    </header>
  );

  if (auth.phase === 'checking') {
    return (
      <div className="app">
        {topbar}
        <main>
          <div className="state" role="status" aria-busy="true">
            Signing in…
          </div>
        </main>
      </div>
    );
  }

  if (auth.phase === 'signed-out' || auth.phase === 'redirecting') {
    return (
      <div className="app">
        {topbar}
        <main>
          <SignIn
            onSignIn={beginSignIn}
            busy={auth.phase === 'redirecting'}
            error={auth.phase === 'signed-out' ? auth.error : undefined}
          />
        </main>
      </div>
    );
  }

  if (selected) {
    return (
      <div className="app">
        {topbar}
        <main>
          <ArtifactDetail cfgId={selected} onBack={closeArtifact} onOpen={openArtifact} />
        </main>
      </div>
    );
  }

  return (
    <div className="app">
      {topbar}

      <main>
        <FilterBar
          filter={filter}
          onChange={setFilter}
          onClear={clear}
          resultCount={items.length}
          loading={loading}
        />

        {problem && <ErrorState problem={problem} onRetry={retry} />}

        {!problem && showSkeleton && (
          <table className="estate">
            <thead>
              <tr>
                <th scope="col">Artifact</th>
                <th scope="col">Authority</th>
                <th scope="col">Role</th>
                <th scope="col">Lifecycle</th>
                <th scope="col">Version</th>
              </tr>
            </thead>
            <SkeletonRows />
          </table>
        )}

        {!problem && !showSkeleton && items.length === 0 && !filtered && <EmptyEstate />}
        {!problem && !showSkeleton && items.length === 0 && filtered && (
          <EmptyResults onClear={clear} />
        )}

        {!problem && items.length > 0 && (
          <>
            <EstateTable items={items} onOpen={openArtifact} />
            {cursor && (
              <div className="more">
                <button type="button" className="btn" onClick={loadMore} disabled={loading}>
                  {loading ? 'Loading…' : 'Load more'}
                </button>
              </div>
            )}
          </>
        )}
      </main>
    </div>
  );
}
