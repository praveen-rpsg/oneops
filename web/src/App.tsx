import { lazy, useCallback, useEffect, useState } from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';
import { completeSignIn, fetchAuthConfig, signIn } from './auth';
import type { AuthConfig } from './auth';
import { Shell } from './Shell';
import { SignIn } from './components/SignIn';
import Box from '@cloudscape-design/components/box';
import Spinner from '@cloudscape-design/components/spinner';

// Route pages are lazy-loaded so the initial bundle is the shell + landing
// route only; each screen's code (and, for Dashboards/Topology, its heavier
// chart/SVG dependencies) is fetched on navigation. Pages use named exports
// (their own tests import them by name), so `React.lazy` — which requires a
// default export — is adapted per import rather than changing the pages.
const AdministrationPage = lazy(() =>
  import('./routes/AdministrationPage').then((m) => ({ default: m.AdministrationPage })),
);
const AlertsBoardPage = lazy(() => import('./routes/AlertsBoardPage').then((m) => ({ default: m.AlertsBoardPage })));
const ArtifactPage = lazy(() => import('./routes/ArtifactPage').then((m) => ({ default: m.ArtifactPage })));
const DashboardsPage = lazy(() => import('./routes/DashboardsPage').then((m) => ({ default: m.DashboardsPage })));
const EscalationBoardPage = lazy(() =>
  import('./routes/EscalationBoardPage').then((m) => ({ default: m.EscalationBoardPage })),
);
const EstatePage = lazy(() => import('./routes/EstatePage').then((m) => ({ default: m.EstatePage })));
const IncidentBoardPage = lazy(() =>
  import('./routes/IncidentBoardPage').then((m) => ({ default: m.IncidentBoardPage })),
);
const MaintenanceBoardPage = lazy(() =>
  import('./routes/MaintenanceBoardPage').then((m) => ({ default: m.MaintenanceBoardPage })),
);
const MembersPage = lazy(() => import('./routes/MembersPage').then((m) => ({ default: m.MembersPage })));
const NOCOverviewPage = lazy(() => import('./routes/NOCOverviewPage').then((m) => ({ default: m.NOCOverviewPage })));
const OnCallBoardPage = lazy(() => import('./routes/OnCallBoardPage').then((m) => ({ default: m.OnCallBoardPage })));
const TopologyPage = lazy(() => import('./routes/TopologyPage').then((m) => ({ default: m.TopologyPage })));
const UsersPage = lazy(() => import('./routes/UsersPage').then((m) => ({ default: m.UsersPage })));

type AuthState =
  | { phase: 'checking' }
  | { phase: 'signed-out'; cfg: AuthConfig; error?: string }
  | { phase: 'redirecting'; cfg: AuthConfig }
  | { phase: 'ready' };

export default function App() {
  const [auth, setAuth] = useState<AuthState>({ phase: 'checking' });

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
          if (!cancelled) setAuth({ phase: 'ready' });
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

  if (auth.phase === 'checking') {
    return (
      <Box margin="xxl" textAlign="center" padding="xxl">
        <div role="status" aria-busy="true">
          <Spinner size="large" /> Signing in…
        </div>
      </Box>
    );
  }

  if (auth.phase === 'signed-out' || auth.phase === 'redirecting') {
    return (
      <SignIn
        onSignIn={beginSignIn}
        busy={auth.phase === 'redirecting'}
        error={auth.phase === 'signed-out' ? auth.error : undefined}
      />
    );
  }

  return (
    <Routes>
      <Route element={<Shell />}>
        <Route index element={<EstatePage />} />
        <Route path="artifacts/:id" element={<ArtifactPage />} />
        <Route path="noc" element={<NOCOverviewPage />} />
        <Route path="incidents" element={<IncidentBoardPage />} />
        <Route path="alerts" element={<AlertsBoardPage />} />
        <Route path="maintenance" element={<MaintenanceBoardPage />} />
        <Route path="on-call" element={<OnCallBoardPage />} />
        <Route path="escalation" element={<EscalationBoardPage />} />
        <Route path="topology" element={<TopologyPage />} />
        <Route path="dashboards" element={<DashboardsPage />} />
        <Route path="administration" element={<AdministrationPage />} />
        <Route path="members" element={<MembersPage />} />
        <Route path="users" element={<UsersPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
