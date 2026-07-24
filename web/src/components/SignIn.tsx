interface Props {
  onSignIn: () => void;
  /** Set when a previous attempt failed, or when the deployment is misconfigured. */
  error?: string;
  busy: boolean;
}

export function SignIn({ onSignIn, error, busy }: Props) {
  return (
    <div className="state signin" role="main">
      <h2>Sign in to OneOps</h2>
      <p>You will be redirected to your organisation&rsquo;s identity provider.</p>

      {error && (
        <p className="signin-error" role="alert">
          {error}
        </p>
      )}

      <button type="button" className="btn btn-primary" onClick={onSignIn} disabled={busy}>
        {busy ? 'Redirecting…' : 'Sign in'}
      </button>
    </div>
  );
}
