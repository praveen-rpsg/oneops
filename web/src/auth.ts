// OIDC Authorization Code + PKCE for the browser console.
//
// The console only *carries* a token; it never validates one. Signature,
// issuer and audience checks are the server's (internal/auth), which is where
// they belong and where they are already enforced. That keeps this file small
// and keeps the security-critical path in one place.
//
// The access token is held in memory only — never localStorage — so it cannot
// be read by injected script or survive a tab close.

export interface AuthConfig {
  auth_enabled: boolean;
  issuer?: string;
  audience?: string;
  client_id?: string;
}

interface Discovery {
  authorization_endpoint: string;
  token_endpoint: string;
  end_session_endpoint?: string;
}

const VERIFIER_KEY = 'oneops.pkce.verifier';
const STATE_KEY = 'oneops.pkce.state';
const RETURN_KEY = 'oneops.return_to';

let accessToken: string | null = null;
let subject: string | null = null;
let roles: string[] = [];

export const getToken = () => accessToken;
export const getSubject = () => subject;

/**
 * DISPLAY-ONLY. Returns the `roles` claim carried by the current token, for
 * the Administration "who am I" screen (E-ID.1). This is never the basis for
 * an authorization decision — the server (internal/auth) is the sole
 * authority, exactly as the file header says for the subject and every other
 * claim. Use it only to label or navigate the UI, never to gate an action.
 */
export const getRoles = () => roles;

function decodeJwtPayload(jwt: string): Record<string, unknown> | null {
  try {
    const [, payload] = jwt.split('.');
    return JSON.parse(
      decodeURIComponent(
        atob(payload.replace(/-/g, '+').replace(/_/g, '/'))
          .split('')
          .map((c) => `%${c.charCodeAt(0).toString(16).padStart(2, '0')}`)
          .join(''),
      ),
    ) as Record<string, unknown>;
  } catch {
    return null;
  }
}

/** Claims the console displays. Not trusted for authorization — the server decides that. */
function readSubject(jwt: string): string | null {
  const json = decodeJwtPayload(jwt) as { sub?: string; email?: string; preferred_username?: string } | null;
  return json?.preferred_username ?? json?.email ?? json?.sub ?? null;
}

/**
 * DISPLAY-ONLY (see getRoles). A malformed token, or one with no/non-array
 * `roles` claim, yields `[]` rather than throwing — the identity panel must
 * degrade, never crash.
 */
function readRoles(jwt: string): string[] {
  const json = decodeJwtPayload(jwt) as { roles?: unknown } | null;
  const claim = json?.roles;
  if (!Array.isArray(claim)) return [];
  return claim.filter((r): r is string => typeof r === 'string');
}

export async function fetchAuthConfig(): Promise<AuthConfig> {
  const res = await fetch('/auth/config', { headers: { Accept: 'application/json' } });
  if (!res.ok) throw new Error(`auth config unavailable (${res.status})`);
  return (await res.json()) as AuthConfig;
}

async function discover(issuer: string): Promise<Discovery> {
  const url = `${issuer.replace(/\/$/, '')}/.well-known/openid-configuration`;
  const res = await fetch(url);
  if (!res.ok) throw new Error(`identity provider discovery failed (${res.status})`);
  return (await res.json()) as Discovery;
}

function randomString(bytes = 32): string {
  const a = new Uint8Array(bytes);
  crypto.getRandomValues(a);
  return btoa(String.fromCharCode(...a)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=/g, '');
}

async function challenge(verifier: string): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier));
  return btoa(String.fromCharCode(...new Uint8Array(digest)))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=/g, '');
}

const redirectUri = () => `${window.location.origin}/`;

/** Redirects to the identity provider. Does not return. */
export async function signIn(cfg: AuthConfig): Promise<void> {
  if (!cfg.issuer || !cfg.client_id) {
    throw new Error('This deployment has no identity provider configured.');
  }
  const { authorization_endpoint } = await discover(cfg.issuer);

  const verifier = randomString();
  const state = randomString(16);
  sessionStorage.setItem(VERIFIER_KEY, verifier);
  sessionStorage.setItem(STATE_KEY, state);
  sessionStorage.setItem(RETURN_KEY, window.location.search);

  const p = new URLSearchParams({
    response_type: 'code',
    client_id: cfg.client_id,
    redirect_uri: redirectUri(),
    scope: 'openid profile email',
    state,
    code_challenge: await challenge(verifier),
    code_challenge_method: 'S256',
  });
  if (cfg.audience) p.set('audience', cfg.audience);

  window.location.assign(`${authorization_endpoint}?${p.toString()}`);
}

/**
 * Completes the redirect when the provider sends us back with ?code=.
 * Returns true when a session was established.
 */
export async function completeSignIn(cfg: AuthConfig): Promise<boolean> {
  const params = new URLSearchParams(window.location.search);
  const code = params.get('code');
  const state = params.get('state');
  if (!code) return false;

  const verifier = sessionStorage.getItem(VERIFIER_KEY);
  const expected = sessionStorage.getItem(STATE_KEY);
  sessionStorage.removeItem(VERIFIER_KEY);
  sessionStorage.removeItem(STATE_KEY);

  // CSRF defence: the provider must echo the state we generated.
  if (!verifier || !expected || state !== expected) {
    throw new Error('Sign-in could not be verified. Please try again.');
  }
  if (!cfg.issuer || !cfg.client_id) throw new Error('No identity provider configured.');

  const { token_endpoint } = await discover(cfg.issuer);
  const res = await fetch(token_endpoint, {
    method: 'POST',
    headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
    body: new URLSearchParams({
      grant_type: 'authorization_code',
      code,
      redirect_uri: redirectUri(),
      client_id: cfg.client_id,
      code_verifier: verifier,
    }),
  });
  if (!res.ok) throw new Error(`Sign-in failed (${res.status}).`);

  const body = (await res.json()) as { access_token?: string; id_token?: string };
  const token = body.access_token ?? body.id_token;
  if (!token) throw new Error('The identity provider returned no token.');

  setToken(token);

  // Restore the view the user was on before being redirected away.
  const back = sessionStorage.getItem(RETURN_KEY) ?? '';
  sessionStorage.removeItem(RETURN_KEY);
  window.history.replaceState(null, '', back || window.location.pathname);
  return true;
}

/** Used by the redirect flow, and directly by tests. */
export function setToken(token: string | null) {
  accessToken = token;
  subject = token ? readSubject(token) : null;
  roles = token ? readRoles(token) : [];
}

export function signOut() {
  setToken(null);
  sessionStorage.clear();
  window.location.assign('/');
}
