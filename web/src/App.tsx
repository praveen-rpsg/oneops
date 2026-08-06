import { useCallback, useEffect, useState } from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';
import { completeSignIn, fetchAuthConfig, signIn } from './auth';
import type { AuthConfig } from './auth';
import { Shell } from './Shell';
import { ArtifactPage } from './routes/ArtifactPage';
import { ComingSoon } from './routes/ComingSoon';
import { EstatePage } from './routes/EstatePage';
import { NOCOverviewPage } from './routes/NOCOverviewPage';
import { SignIn } from './components/SignIn';
import Box from '@cloudscape-design/components/box';
import Spinner from '@cloudscape-design/components/spinner';

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
        <Route path="incidents" element={<ComingSoon title="Incidents" />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Route>
    </Routes>
  );
}
