import { useCallback, useState } from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import SpaceBetween from '@cloudscape-design/components/space-between';
import { fetchAuthConfig, signIn } from '../auth';
import { redeemInvitation } from '../invitations';

/** The literal generic failure ADR-IAC-004 specifies: every redeem failure cause is byte-identical by design. */
const GENERIC_FAILURE = 'This invitation link is invalid or has expired.';

type Status = 'idle' | 'pending' | 'success' | 'error';

/**
 * The invitee's REDEEM screen (E-ID.5, over E-ID.4b's `POST
 * /auth/invitations/redeem`, ADR-IAC-004). This is the one screen in the
 * console that MUST render for a visitor who has never signed in — that is
 * the entire reason the endpoint underneath is unauthenticated. It is
 * reached at the top-level `/redeem` route BEFORE `App`'s ordinary
 * checking/signed-out/ready auth gate (see `App.tsx`'s public-route bypass),
 * renders standalone (no `Shell`, no session, no nav — there is nothing to
 * navigate to yet), and never sends a bearer token: `redeemInvitation` calls
 * `postJSON`, which only attaches `Authorization` when a token exists, and
 * an invitee here has none.
 *
 * Per ADR-IAC-004, every failure cause (unknown/expired/revoked/redeemed
 * token, or one naming a suspended/deactivated account) is the SAME generic
 * `400` — this screen deliberately shows one generic message for all of them
 * rather than trying to distinguish an enumeration oracle the server was
 * built specifically not to have.
 */
export function RedeemPage() {
  const [token, setToken] = useState(() => new URLSearchParams(window.location.search).get('token') ?? '');
  const [status, setStatus] = useState<Status>('idle');
  const [organization, setOrganization] = useState<string | null>(null);
  const [signInBusy, setSignInBusy] = useState(false);
  const [signInError, setSignInError] = useState<string | undefined>();

  const trimmed = token.trim();
  const canSubmit = trimmed.length > 0 && status !== 'pending';

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setStatus('pending');
    try {
      const res = await redeemInvitation(trimmed);
      setOrganization(res.organization);
      setStatus('success');
    } catch (err) {
      // Deliberately not branching on err.problem.status/detail — see the
      // module doc comment. Any failure (network or the server's uniform
      // 400) renders the same generic message.
      void err;
      setStatus('error');
    }
  }, [canSubmit, trimmed]);

  const beginSignIn = useCallback(async () => {
    setSignInBusy(true);
    setSignInError(undefined);
    try {
      const cfg = await fetchAuthConfig();
      if (!cfg.auth_enabled) {
        // Local/dev deployments with auth disabled: there is no provider to
        // redirect to, so send the invitee straight into the console, where
        // `App` proceeds directly to 'ready'.
        window.location.assign('/');
        return;
      }
      await signIn(cfg); // Redirects the browser; does not return on success.
    } catch (err) {
      setSignInBusy(false);
      setSignInError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  return (
    <Box margin={{ vertical: 'xxl' }} textAlign="center">
      <div style={{ maxWidth: 460, margin: '0 auto', textAlign: 'left' }}>
        <Container header={<Header variant="h2">{status === 'success' ? 'Invitation accepted' : 'Accept your invitation'}</Header>}>
          <SpaceBetween size="m">
            {status !== 'success' && (
              <Box>
                Paste the invitation token below, or open this page from the link you were sent — the token is
                filled in automatically.
              </Box>
            )}

            {status === 'error' && (
              <div role="alert">
                <Alert type="error">{GENERIC_FAILURE}</Alert>
              </div>
            )}

            {status !== 'success' && (
              <FormField label="Invitation token">
                <Input
                  value={token}
                  onChange={({ detail }) => setToken(detail.value)}
                  ariaLabel="Invitation token"
                  disabled={status === 'pending'}
                  placeholder="Required"
                />
              </FormField>
            )}

            {status !== 'success' && (
              <Button variant="primary" onClick={() => void submit()} loading={status === 'pending'} disabled={!canSubmit}>
                Accept invitation
              </Button>
            )}

            {status === 'success' && (
              <Box>
                You&rsquo;ve joined <b>{organization}</b>. Sign in to continue.
              </Box>
            )}

            {status === 'success' && signInError && (
              <div role="alert">
                <Alert type="error">{signInError}</Alert>
              </div>
            )}

            {status === 'success' && (
              <Button variant="primary" onClick={() => void beginSignIn()} loading={signInBusy}>
                Sign in
              </Button>
            )}
          </SpaceBetween>
        </Container>
      </div>
    </Box>
  );
}
