import { Suspense, lazy, useCallback, useEffect, useState } from 'react';
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
const InvitationsPage = lazy(() => import('./routes/InvitationsPage').then((m) => ({ default: m.InvitationsPage })));
const MaintenanceBoardPage = lazy(() =>
  import('./routes/MaintenanceBoardPage').then((m) => ({ default: m.MaintenanceBoardPage })),
);
const MembersPage = lazy(() => import('./routes/MembersPage').then((m) => ({ default: m.MembersPage })));
const NOCOverviewPage = lazy(() => import('./routes/NOCOverviewPage').then((m) => ({ default: m.NOCOverviewPage })));
const OnCallBoardPage = lazy(() => import('./routes/OnCallBoardPage').then((m) => ({ default: m.OnCallBoardPage })));
// `/redeem` (E-ID.5, ADR-IAC-004) is reached via the PUBLIC-ROUTE bypass
// below, never through the authenticated <Routes> tree — an invitee has no
// session, so it cannot live inside the `Shell`-wrapped routes the auth gate
// protects. Still lazy-loaded, same as every other route's chunk.
const RedeemPage = lazy(() => import('./routes/RedeemPage').then((m) => ({ default: m.RedeemPage })));
const TopologyPage = lazy(() => import('./routes/TopologyPage').then((m) => ({ default: m.TopologyPage })));
const UsersPage = lazy(() => import('./routes/UsersPage').then((m) => ({ default: m.UsersPage })));
const VulnerabilitiesPage = lazy(() =>
  import('./routes/VulnerabilitiesPage').then((m) => ({ default: m.VulnerabilitiesPage })),
);

/** Shared with `Shell.tsx`'s ROUTE_LOADING_FALLBACK idiom; used here for the pre-auth `/redeem` Suspense boundary, which has no Shell to host it. */
const PUBLIC_ROUTE_LOADING_FALLBACK = (
  <Box margin="xxl" textAlign="center" padding="xxl">
    <div role="status" aria-busy="true">
      <Spinner size="large" /> Loading…
    </div>
  </Box>
);

type AuthState =
  | { phase: 'checking' }
  | { phase: 'signed-out'; cfg: AuthConfig; error?: string }
  | { phase: 'redirecting'; cfg: AuthConfig }
  | { phase: 'ready' };

export default function App() {
  const [auth, setAuth] = useState<AuthState>({ phase: 'checking' });

  // PUBLIC ROUTE (E-ID.5, ADR-IAC-004): an invitee arriving at /redeem has no
  // session, and may never have had one — that is the entire reason
  // `POST /auth/invitations/redeem` is unauthenticated. It must render
  // BEFORE, and independently of, every phase below (checking/signed-out/
  // ready), and must not require /auth/config to have resolved. A plain
  // pathname check rather than a react-router <Route> because it has to win
  // ahead of the auth gate, not live inside the authenticated <Routes> tree
  // that gate protects.
  const isPublicRoute = window.location.pathname === '/redeem';

  // Establish the session before any data request is made. When the deployment
  // runs with auth disabled (local development) the console proceeds directly.
  // Skipped entirely for the public route: it needs none of this state.
  useEffect(() => {
    if (isPublicRoute) return;
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
  }, [isPublicRoute]);

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

  if (isPublicRoute) {
    return (
      <Suspense fallback={PUBLIC_ROUTE_LOADING_FALLBACK}>
        <RedeemPage />
      </Suspense>
    );
  }

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
        <Route path="security/vulnerabilities" element={<VulnerabilitiesPage />} />
        <Route path="administration" element={<AdministrationPage />} />
        <Route path="members" element={<MembersPage />} />
        <Route path="users" element={<UsersPage />} />
        <Route path="invitations" element={<InvitationsPage />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
