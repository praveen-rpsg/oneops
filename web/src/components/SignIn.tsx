import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import Header from '@cloudscape-design/components/header';
import SpaceBetween from '@cloudscape-design/components/space-between';

interface Props {
  onSignIn: () => void;
  /** Set when a previous attempt failed, or when the deployment is misconfigured. */
  error?: string;
  busy: boolean;
}

export function SignIn({ onSignIn, error, busy }: Props) {
  return (
    <Box margin={{ vertical: 'xxl' }} textAlign="center">
      <div style={{ maxWidth: 460, margin: '0 auto', textAlign: 'left' }}>
        <Container header={<Header variant="h2">Sign in to OneOps</Header>}>
          <SpaceBetween size="m">
            <Box>You will be redirected to your organisation&rsquo;s identity provider.</Box>

            {error && (
              <div role="alert">
                <Alert type="error">{error}</Alert>
              </div>
            )}

            <Button variant="primary" onClick={onSignIn} loading={busy}>
              {busy ? 'Redirecting…' : 'Sign in'}
            </Button>
          </SpaceBetween>
        </Container>
      </div>
    </Box>
  );
}
