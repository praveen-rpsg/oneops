import { useCallback, useEffect, useMemo, useState } from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import CopyToClipboard from '@cloudscape-design/components/copy-to-clipboard';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import type { TableProps } from '@cloudscape-design/components/table';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { ErrorState } from '../components/States';
import {
  INVITATION_LIST_CAP,
  createInvitation,
  listInvitations,
  revokeInvitation,
} from '../invitations';
import type { CreateInvitationResponse, InvitationDTO, InvitationStatus } from '../invitations';

/**
 * Email shape restated from `domain.emailPattern` — see `UsersPage.tsx`'s
 * identical constants for why this is a client-side courtesy only; the
 * server (`createInvitation`'s length check + `domain.Invitation.Validate`)
 * is the final authority.
 */
const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const MAX_EMAIL_LENGTH = 254;

function emailError(trimmed: string): string | undefined {
  if (trimmed.length === 0) return 'Email is required.';
  if (trimmed.length > MAX_EMAIL_LENGTH) return `Must be at most ${MAX_EMAIL_LENGTH} characters.`;
  if (!EMAIL_PATTERN.test(trimmed)) return 'Must be a valid email address.';
  return undefined;
}

const STATUS_TYPE: Record<InvitationStatus, StatusIndicatorProps.Type> = {
  pending: 'pending',
  redeemed: 'success',
  revoked: 'stopped',
  expired: 'warning',
};

const STATUS_LABEL: Record<InvitationStatus, string> = {
  pending: 'Pending',
  redeemed: 'Redeemed',
  revoked: 'Revoked',
  expired: 'Expired',
};

/** The clean, honest 403: the server's own refusal, never a crash or a raw dump. */
function PermissionNeeded() {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>You need tenant-admin permission to manage invitations.</b>
        <Box variant="p" color="text-body-secondary">
          Your account does not currently hold the permission OneOps&apos; invitation administration endpoints
          require. Ask your tenant administrator to grant it.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

function InvitationsEmpty() {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No invitations yet</b>
        <Box variant="p" color="text-body-secondary">
          Invite someone by email to bring them into your organisation.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

/** Builds the shareable link an admin can hand an invitee directly, skipping manual token entry. */
function redeemLink(token: string): string {
  return `${window.location.origin}/redeem?token=${encodeURIComponent(token)}`;
}

/**
 * The one-time reveal (ADR-IAC-003): the token exists ONLY in the create
 * response, never again — this is the single place in the entire console an
 * admin can ever see or copy it. Closing this dialog in any way (Done, the
 * dismiss X) is treated as acknowledged and reloads the list; there is
 * nothing to "go back" to.
 */
function TokenRevealModal({ result, onDone }: { result: CreateInvitationResponse; onDone: () => void }) {
  const link = redeemLink(result.token);
  return (
    <Modal
      visible
      header="Invitation created"
      onDismiss={onDone}
      closeAriaLabel="Dismiss"
      footer={
        <Box float="right">
          <Button variant="primary" onClick={onDone}>
            Done
          </Button>
        </Box>
      }
    >
      <SpaceBetween size="m">
        <div role="alert">
          <Alert type="warning" header="This link is shown once — copy it now; it cannot be retrieved later.">
            OneOps stores only a hash of this token. If it is lost, revoke this invitation and issue a new one.
          </Alert>
        </div>
        <FormField label="One-time token">
          <SpaceBetween direction="horizontal" size="xs">
            <Box fontSize="body-m" padding={{ vertical: 'xs' }}>
              <code>{result.token}</code>
            </Box>
            <CopyToClipboard
              variant="icon"
              textToCopy={result.token}
              copyButtonAriaLabel="Copy token"
              copySuccessText="Token copied"
              copyErrorText="Token could not be copied"
            />
          </SpaceBetween>
        </FormField>
        <FormField label="Shareable redeem link" description={`Send this to ${result.email} — it carries the token.`}>
          <SpaceBetween direction="horizontal" size="xs">
            <Box fontSize="body-s" padding={{ vertical: 'xs' }}>
              <code>{link}</code>
            </Box>
            <CopyToClipboard
              variant="icon"
              textToCopy={link}
              copyButtonAriaLabel="Copy redeem link"
              copySuccessText="Link copied"
              copyErrorText="Link could not be copied"
            />
          </SpaceBetween>
        </FormField>
      </SpaceBetween>
    </Modal>
  );
}

function CreateInvitationModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (result: CreateInvitationResponse) => void;
}) {
  const [email, setEmail] = useState('');
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const trimmed = email.trim();
  const emailErrorText = email.length > 0 ? emailError(trimmed) : undefined;
  const canSubmit = emailError(trimmed) === undefined && !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      const result = await createInvitation({ email: trimmed });
      onCreated(result);
    } catch (err) {
      setProblem(err instanceof ApiError ? err.problem : { title: 'Invite failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [canSubmit, trimmed, onCreated]);

  return (
    <Modal
      visible
      header="Invite by email"
      onDismiss={() => {
        if (!busy) onClose();
      }}
      closeAriaLabel="Dismiss"
      footer={
        <Box float="right">
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button variant="primary" loading={busy} disabled={!canSubmit} onClick={() => void submit()}>
              Invite
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Form>
        <SpaceBetween size="m">
          {problem && (
            <div role="alert">
              <Alert type="error" header="Could not create the invitation">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <FormField
            label="Email"
            errorText={emailErrorText}
            description="Invites this address into your own organisation. The invitation expires after 7 days."
          >
            <Input
              value={email}
              onChange={({ detail }) => setEmail(detail.value)}
              disabled={busy}
              ariaLabel="Email"
              placeholder="Required"
            />
          </FormField>
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

/**
 * Invitations (E-ID.5, over E-ID.4a's admin endpoints, ADR-IAC-003): a
 * tenant admin invites someone by email, sees the one-time token and a
 * shareable redeem link exactly once, and reviews/revokes pending
 * invitations. Follows the write-action pattern ADR-ACT-001 established
 * (confirm-before-revoke, disable-while-pending, refetch-after, inline RFC
 * 7807 errors) and the accept-no-row_version-on-delete decision ADR-HARD-003
 * made for this exact shape of endpoint (`revokeInvitation` takes no
 * row_version, like `revokeMembership`).
 *
 * Unlike `MembersPage`, there is no org_id to resolve here at all: every
 * endpoint this screen calls resolves the caller's own organisation from
 * context server-side (ADR-IAC-003) — the screen has nothing to look up
 * before it can load.
 */
export function InvitationsPage() {
  const [items, setItems] = useState<InvitationDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);

  const [createOpen, setCreateOpen] = useState(false);
  const [tokenReveal, setTokenReveal] = useState<CreateInvitationResponse | null>(null);
  const [revokeTarget, setRevokeTarget] = useState<InvitationDTO | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionProblem, setActionProblem] = useState<ProblemDetail | null>(null);

  const reload = useCallback(() => setReloads((n) => n + 1), []);

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);
    listInvitations(INVITATION_LIST_CAP, ctrl.signal)
      .then((page) => {
        if (ctrl.signal.aborted) return;
        setItems(page.items ?? []);
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(
          err instanceof ApiError ? err.problem : { title: 'Could not load invitations', status: 0, detail: String(err) },
        );
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });
    return () => ctrl.abort();
  }, [reloads]);

  const forbidden = problem !== null && problem.status === 403;

  const runRevoke = useCallback(async () => {
    if (!revokeTarget) return;
    setBusy(true);
    setActionProblem(null);
    try {
      await revokeInvitation(revokeTarget.invitation_id);
      setRevokeTarget(null);
      reload();
    } catch (err) {
      setRevokeTarget(null);
      setActionProblem(err instanceof ApiError ? err.problem : { title: 'Revoke failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [revokeTarget, reload]);

  const columns = useMemo<TableProps.ColumnDefinition<InvitationDTO>[]>(
    () => [
      {
        id: 'email',
        header: 'Email',
        isRowHeader: true,
        sortingComparator: (a, b) => a.email.localeCompare(b.email),
        cell: (i) => i.email,
      },
      {
        id: 'status',
        header: 'Status',
        sortingComparator: (a, b) => a.status.localeCompare(b.status),
        cell: (i) => <StatusIndicator type={STATUS_TYPE[i.status]}>{STATUS_LABEL[i.status]}</StatusIndicator>,
      },
      {
        id: 'expires',
        header: 'Expires',
        sortingComparator: (a, b) => new Date(a.expires_at).getTime() - new Date(b.expires_at).getTime(),
        cell: (i) => new Date(i.expires_at).toLocaleString(),
      },
      {
        id: 'created',
        header: 'Created',
        sortingComparator: (a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime(),
        cell: (i) => new Date(i.created_at).toLocaleString(),
      },
      {
        id: 'actions',
        header: 'Actions',
        cell: (i) =>
          i.status === 'pending' ? (
            <Button disabled={busy} onClick={() => setRevokeTarget(i)}>
              Revoke
            </Button>
          ) : (
            <Box color="text-body-secondary">—</Box>
          ),
      },
    ],
    [busy],
  );

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Invite someone into your organisation by email. The one-time token and redeem link are shown exactly once, when the invitation is created."
        counter={!forbidden ? `(${items.length})` : undefined}
        actions={
          <Button onClick={() => setCreateOpen(true)} disabled={forbidden}>
            Invite by email
          </Button>
        }
      >
        Invitations
      </Header>

      {actionProblem && (
        <div role="alert">
          <Alert type="error" header="Action failed" dismissible onDismiss={() => setActionProblem(null)}>
            {actionProblem.detail ?? `The server returned ${actionProblem.status}.`}
          </Alert>
        </div>
      )}

      {forbidden && <PermissionNeeded />}

      {!forbidden && problem && <ErrorState problem={problem} onRetry={reload} />}

      {!forbidden && !problem && (
        <Table
          items={items}
          columnDefinitions={columns}
          trackBy="invitation_id"
          loading={loading}
          loadingText="Loading invitations"
          variant="container"
          ariaLabels={{ tableLabel: 'Invitations' }}
          empty={<InvitationsEmpty />}
        />
      )}

      {createOpen && (
        <CreateInvitationModal
          onClose={() => setCreateOpen(false)}
          onCreated={(result) => {
            setCreateOpen(false);
            setTokenReveal(result);
          }}
        />
      )}

      {tokenReveal && (
        <TokenRevealModal
          result={tokenReveal}
          onDone={() => {
            setTokenReveal(null);
            reload();
          }}
        />
      )}

      {revokeTarget && (
        <Modal
          visible
          header={`Revoke the invitation to ${revokeTarget.email}?`}
          onDismiss={() => {
            if (!busy) setRevokeTarget(null);
          }}
          closeAriaLabel="Dismiss"
          footer={
            <Box float="right">
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="link" onClick={() => setRevokeTarget(null)} disabled={busy}>
                  Keep it
                </Button>
                <Button variant="primary" loading={busy} onClick={() => void runRevoke()}>
                  Revoke invitation
                </Button>
              </SpaceBetween>
            </Box>
          }
        >
          <Box>
            This withdraws the invitation before it is redeemed — the token stops working immediately. Like the other
            config deletes, there is no optimistic-lock check here (ADR-HARD-003) — the confirmation above is the
            guard.
          </Box>
        </Modal>
      )}
    </SpaceBetween>
  );
}
