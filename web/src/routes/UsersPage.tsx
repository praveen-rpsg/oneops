import { useCallback, useEffect, useMemo, useState } from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
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
  createUser,
  legalUserTransitions,
  listUsers,
  renameUser,
  setUserStatus,
  USER_LIST_CAP,
} from '../users_admin';
import type { UserDTO, UserStatus } from '../users_admin';

/**
 * Email shape restated from `domain.emailPattern`
 * (`internal/domain/user.go:69`) — deliberately permissive, the same
 * "reject the shapes that are certainly wrong, leave the rest to delivery"
 * posture the Go comment documents. `MAX_EMAIL_LENGTH`/`MAX_DISPLAY_NAME_LENGTH`
 * restate `domain.MaxEmailLength`/`domain.MaxDisplayNameLength`
 * (`user.go:72,75`). This is a client-side courtesy only: the server
 * validates independently and is the final authority (`domain.User.Validate`,
 * `user.go:101`).
 */
const EMAIL_PATTERN = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const MAX_EMAIL_LENGTH = 254;
const MAX_DISPLAY_NAME_LENGTH = 200;

function emailError(trimmed: string): string | undefined {
  if (trimmed.length === 0) return 'Email is required.';
  if (trimmed.length > MAX_EMAIL_LENGTH) return `Must be at most ${MAX_EMAIL_LENGTH} characters.`;
  if (!EMAIL_PATTERN.test(trimmed)) return 'Must be a valid email address.';
  return undefined;
}

function displayNameError(value: string): string | undefined {
  return value.length > MAX_DISPLAY_NAME_LENGTH ? `Must be at most ${MAX_DISPLAY_NAME_LENGTH} characters.` : undefined;
}

const STATUS_TYPE: Record<UserStatus, StatusIndicatorProps.Type> = {
  invited: 'pending',
  active: 'success',
  suspended: 'warning',
  deactivated: 'stopped',
};

const STATUS_LABEL: Record<UserStatus, string> = {
  invited: 'Invited',
  active: 'Active',
  suspended: 'Suspended',
  deactivated: 'Deactivated',
};

/** Label for a status-transition BUTTON, keyed by its TARGET status. No status ever transitions to `invited` (`USER_TRANSITIONS`), so it needs none. */
const TRANSITION_LABEL: Partial<Record<UserStatus, string>> = {
  active: 'Activate',
  suspended: 'Suspend',
  deactivated: 'Deactivate',
};

/**
 * Consequential transitions (ADR-ACT-001 §1 pattern): confirmed via `Modal`
 * before sending. `deactivated` is the only one — it is TERMINAL
 * (`USER_TRANSITIONS`, ADR-IDENTITY-001 §8.3), unlike suspend/activate which
 * are freely reversible between each other. Every other legal transition
 * fires directly — still disabled-while-pending and still error-surfacing,
 * just without an extra confirmation click for a reversible move.
 */
const CONSEQUENTIAL: Partial<Record<UserStatus, string>> = {
  deactivated:
    'This permanently deactivates the user. Deactivation is TERMINAL and cannot be undone — there is no ' +
    'transition back to any other status; the row is kept only so audit events can still name its author. If ' +
    'access is needed again, a new invitation must be issued.',
};

/** The clean, honest 403: the server's own refusal, never a crash or a raw dump. */
function PermissionNeeded() {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>You need platform-admin permission to manage the user registry.</b>
        <Box variant="p" color="text-body-secondary">
          Your account does not currently hold the permission OneOps&apos; global user-registry endpoints require —
          a higher bar than tenant administration. Ask a platform administrator to grant it.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

function UsersEmpty() {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No users registered yet</b>
        <Box variant="p" color="text-body-secondary">
          Create one to add them to the platform registry.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

function CreateUserModal({ onClose, onCreated }: { onClose: () => void; onCreated: () => void }) {
  const [email, setEmail] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const trimmedEmail = email.trim();
  const trimmedName = displayName.trim();
  const emailErrorText = email.length > 0 ? emailError(trimmedEmail) : undefined;
  const nameErrorText = displayNameError(trimmedName);
  const canSubmit = emailError(trimmedEmail) === undefined && nameErrorText === undefined && !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      await createUser({ email: trimmedEmail, display_name: trimmedName });
      onCreated();
    } catch (err) {
      setProblem(err instanceof ApiError ? err.problem : { title: 'Create failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [canSubmit, trimmedEmail, trimmedName, onCreated]);

  return (
    <Modal
      visible
      header="Create user"
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
              Create
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Form>
        <SpaceBetween size="m">
          {problem && (
            <div role="alert">
              <Alert type="error" header="Could not create the user">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <FormField label="Email" errorText={emailErrorText}>
            <Input value={email} onChange={({ detail }) => setEmail(detail.value)} disabled={busy} ariaLabel="Email" placeholder="Required" />
          </FormField>
          <FormField
            label="Display name"
            description="Optional"
            errorText={displayName.length > 0 ? nameErrorText : undefined}
          >
            <Input
              value={displayName}
              onChange={({ detail }) => setDisplayName(detail.value)}
              disabled={busy}
              ariaLabel="Display name"
            />
          </FormField>
          <Box color="text-body-secondary" fontSize="body-s">
            The user is created &quot;Invited&quot; and holds no access until a membership is granted.
          </Box>
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

function RenameUserModal({
  user,
  onClose,
  onRenamed,
  onConflict,
}: {
  user: UserDTO;
  onClose: () => void;
  onRenamed: () => void;
  onConflict: () => void;
}) {
  const [displayName, setDisplayName] = useState(user.display_name);
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const trimmedName = displayName.trim();
  const nameErrorText = displayNameError(trimmedName);
  const canSubmit = nameErrorText === undefined && !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      await renameUser(user.user_id, user.row_version, trimmedName);
      onRenamed();
    } catch (err) {
      if (err instanceof ApiError && err.problem.status === 409) {
        onConflict();
        return;
      }
      setProblem(err instanceof ApiError ? err.problem : { title: 'Rename failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [canSubmit, user, trimmedName, onRenamed, onConflict]);

  return (
    <Modal
      visible
      header={`Rename ${user.email}`}
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
              Rename
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Form>
        <SpaceBetween size="m">
          {problem && (
            <div role="alert">
              <Alert type="error" header="Could not rename the user">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <FormField label="Display name" errorText={nameErrorText}>
            <Input
              value={displayName}
              onChange={({ detail }) => setDisplayName(detail.value)}
              disabled={busy}
              ariaLabel="Display name"
            />
          </FormField>
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

/**
 * Users (E-ID.3, ADR-IAC-001 extension): a platform administrator's view of
 * the global user registry, over the endpoints that already exist — list,
 * create, rename, lifecycle transition — following the write-action pattern
 * ADR-ACT-001 established (confirm-before-consequential, disable-while-
 * pending, refetch-after, inline RFC 7807 errors, 409-refetches-with-notice-
 * never-blind-retries) and `IncidentDetailPanel`'s legal-transitions-only
 * shape for the lifecycle buttons.
 *
 * Every endpoint here is `requirePlatformAdmin` (`server.go:403-406`), a
 * strictly higher bar than the `PermAdmin` `MembersPage` uses — most callers
 * will get 403, and this screen degrades to the same clean `PermissionNeeded`
 * state `MembersPage` established rather than crashing or dumping the raw
 * problem.
 */
export function UsersPage() {
  const [items, setItems] = useState<UserDTO[]>([]);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);

  const [createOpen, setCreateOpen] = useState(false);
  const [renameTarget, setRenameTarget] = useState<UserDTO | null>(null);
  const [confirmTarget, setConfirmTarget] = useState<{ user: UserDTO; status: UserStatus } | null>(null);

  const [busyUserId, setBusyUserId] = useState<string | null>(null);
  const [actionProblem, setActionProblem] = useState<ProblemDetail | null>(null);
  const [conflictNotice, setConflictNotice] = useState(false);

  const reload = useCallback(() => setReloads((n) => n + 1), []);

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);
    listUsers(USER_LIST_CAP, ctrl.signal)
      .then((page) => {
        if (ctrl.signal.aborted) return;
        setItems(page.items ?? []);
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(err instanceof ApiError ? err.problem : { title: 'Could not load users', status: 0, detail: String(err) });
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });
    return () => ctrl.abort();
  }, [reloads]);

  const forbidden = problem !== null && problem.status === 403;

  /** Runs after ANY status-change attempt: success and 409 both refetch; only other errors leave the row's state as-is for the operator to read/retry. */
  const runStatusChange = useCallback(
    async (user: UserDTO, target: UserStatus) => {
      setBusyUserId(user.user_id);
      setActionProblem(null);
      try {
        await setUserStatus(user.user_id, user.row_version, target);
        setConfirmTarget(null);
        setConflictNotice(false);
        reload();
      } catch (err) {
        setConfirmTarget(null);
        if (err instanceof ApiError && err.problem.status === 409) {
          setConflictNotice(true);
          reload();
        } else {
          setActionProblem(
            err instanceof ApiError ? err.problem : { title: 'Status change failed', status: 0, detail: String(err) },
          );
        }
      } finally {
        setBusyUserId(null);
      }
    },
    [reload],
  );

  const onStatusClick = useCallback((user: UserDTO, target: UserStatus) => {
    setConflictNotice(false);
    if (CONSEQUENTIAL[target]) {
      setConfirmTarget({ user, status: target });
      return;
    }
    void runStatusChange(user, target);
  }, [runStatusChange]);

  const columns = useMemo<TableProps.ColumnDefinition<UserDTO>[]>(
    () => [
      {
        id: 'email',
        header: 'Email',
        isRowHeader: true,
        sortingComparator: (a, b) => a.email.localeCompare(b.email),
        cell: (u) => (
          <div>
            <Box fontWeight="bold">{u.email}</Box>
            <Box fontSize="body-s" color="text-body-secondary">
              {u.user_id}
            </Box>
          </div>
        ),
      },
      {
        id: 'display_name',
        header: 'Display name',
        sortingComparator: (a, b) => a.display_name.localeCompare(b.display_name),
        cell: (u) => u.display_name || <Box color="text-body-secondary">—</Box>,
      },
      {
        id: 'status',
        header: 'Status',
        sortingComparator: (a, b) => a.status.localeCompare(b.status),
        cell: (u) => <StatusIndicator type={STATUS_TYPE[u.status]}>{STATUS_LABEL[u.status]}</StatusIndicator>,
      },
      {
        id: 'created',
        header: 'Created',
        sortingComparator: (a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime(),
        cell: (u) => new Date(u.created_at).toLocaleString(),
      },
      {
        id: 'updated',
        header: 'Updated',
        sortingComparator: (a, b) => new Date(a.updated_at).getTime() - new Date(b.updated_at).getTime(),
        cell: (u) => new Date(u.updated_at).toLocaleString(),
      },
      {
        id: 'actions',
        header: 'Actions',
        cell: (u) => {
          const rowBusy = busyUserId === u.user_id;
          const transitions = legalUserTransitions(u.status);
          return (
            <SpaceBetween direction="horizontal" size="xs">
              <Button disabled={rowBusy} onClick={() => setRenameTarget(u)}>
                Rename
              </Button>
              {transitions.map((target) => (
                <Button key={target} disabled={rowBusy} loading={rowBusy} onClick={() => onStatusClick(u, target)}>
                  {TRANSITION_LABEL[target] ?? target}
                </Button>
              ))}
            </SpaceBetween>
          );
        },
      },
    ],
    [busyUserId, onStatusClick],
  );

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="The global user registry. Roles are assigned by your identity provider, not here (see Identity & roles) — this screen only creates users and manages their lifecycle status."
        counter={!forbidden ? `(${items.length})` : undefined}
        actions={
          <Button onClick={() => setCreateOpen(true)} disabled={forbidden}>
            Create user
          </Button>
        }
      >
        Users
      </Header>

      {conflictNotice && (
        <div role="alert">
          <Alert type="warning" header="Changed since you loaded it" dismissible onDismiss={() => setConflictNotice(false)}>
            This user was modified concurrently. The list has been reloaded to the current state below — review it and
            try again.
          </Alert>
        </div>
      )}

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
          trackBy="user_id"
          loading={loading}
          loadingText="Loading users"
          variant="container"
          ariaLabels={{ tableLabel: 'Users' }}
          empty={<UsersEmpty />}
        />
      )}

      {createOpen && (
        <CreateUserModal
          onClose={() => setCreateOpen(false)}
          onCreated={() => {
            setCreateOpen(false);
            reload();
          }}
        />
      )}

      {renameTarget && (
        <RenameUserModal
          user={renameTarget}
          onClose={() => setRenameTarget(null)}
          onRenamed={() => {
            setRenameTarget(null);
            reload();
          }}
          onConflict={() => {
            setRenameTarget(null);
            setConflictNotice(true);
            reload();
          }}
        />
      )}

      {confirmTarget && (
        <Modal
          visible
          header={`${TRANSITION_LABEL[confirmTarget.status] ?? confirmTarget.status} ${confirmTarget.user.email}?`}
          onDismiss={() => {
            if (busyUserId === null) setConfirmTarget(null);
          }}
          closeAriaLabel="Dismiss"
          footer={
            <Box float="right">
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="link" onClick={() => setConfirmTarget(null)} disabled={busyUserId !== null}>
                  Cancel
                </Button>
                <Button
                  variant="primary"
                  loading={busyUserId !== null}
                  onClick={() => void runStatusChange(confirmTarget.user, confirmTarget.status)}
                >
                  {TRANSITION_LABEL[confirmTarget.status] ?? confirmTarget.status}
                </Button>
              </SpaceBetween>
            </Box>
          }
        >
          <Box>{CONSEQUENTIAL[confirmTarget.status]}</Box>
        </Modal>
      )}
    </SpaceBetween>
  );
}
