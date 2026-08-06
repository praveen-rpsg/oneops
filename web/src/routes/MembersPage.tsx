import { useCallback, useEffect, useMemo, useState } from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
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
import { MEMBERSHIP_LIST_CAP, grantMembership, listMemberships, revokeMembership } from '../memberships';
import type { MembershipDTO } from '../memberships';
import { listTenantUsers } from '../users';
import type { TenantUserDTO } from '../users';

/**
 * Remembers the organisation the caller is administering, for this TAB's
 * session only — `sessionStorage`, not `localStorage`, matching the
 * no-persistent-client-state posture `theme.ts`/`auth.ts` already establish
 * for this console (the access token itself is memory-only; the theme
 * override is `sessionStorage`). See the doc comment on the input field
 * below for why this exists instead of resolving org_id automatically
 * (ADR-IAC-002).
 */
const ORG_ID_STORAGE_KEY = 'oneops.membership.org_id';

function readStoredOrgId(): string {
  try {
    return window.sessionStorage.getItem(ORG_ID_STORAGE_KEY) ?? '';
  } catch {
    // Storage can throw in private-browsing/locked-down contexts — the field
    // still works for the session, it just starts empty and won't persist.
    return '';
  }
}

function storeOrgId(orgId: string) {
  try {
    if (orgId) window.sessionStorage.setItem(ORG_ID_STORAGE_KEY, orgId);
    else window.sessionStorage.removeItem(ORG_ID_STORAGE_KEY);
  } catch {
    // Best-effort remembering only; see readStoredOrgId.
  }
}

function isForbidden(problem: ProblemDetail): boolean {
  return problem.status === 403;
}

const STATUS_TYPE: Record<MembershipDTO['status'], StatusIndicatorProps.Type> = {
  active: 'success',
  revoked: 'stopped',
};

/**
 * user_id -> display name/email, built from GET /admin/tenant-users
 * (E-ACT.0/.3, `web/src/users.ts`). Membership rows carry only a bare
 * user_id (handlers_memberships.go's membershipDTO has no name field), so
 * this is the same "join at request time for display, never persist it"
 * shape MembershipStore.ListActiveDirectory itself performs server-side.
 * A user this call cannot resolve (its own failure, or a REVOKED member no
 * longer in the ACTIVE directory it reads) simply is not in the map — every
 * lookup site degrades to the raw id.
 */
function indexByUserId(users: TenantUserDTO[]): Record<string, TenantUserDTO> {
  const out: Record<string, TenantUserDTO> = {};
  for (const u of users) out[u.user_id] = u;
  return out;
}

/** The clean, honest 403: the server's own refusal, never a crash or a raw dump. */
function PermissionNeeded() {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>You need tenant-admin permission to manage members.</b>
        <Box variant="p" color="text-body-secondary">
          Your account does not currently hold the permission OneOps&apos; membership administration endpoints
          require. Ask your tenant administrator to grant it.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

function MembersEmpty() {
  return (
    <Box textAlign="center" color="inherit" padding="l">
      <SpaceBetween size="s">
        <b>No memberships for this organisation yet</b>
        <Box variant="p" color="text-body-secondary">
          Grant one to bind a user to it.
        </Box>
      </SpaceBetween>
    </Box>
  );
}

/**
 * "Grant membership" (E-ID.2): the one identifying field
 * grantMembershipRequest takes beyond org_id — user_id — as a plain
 * required Input, not a picker. Confirmed against the Go before writing
 * this (`internal/httpapi/handlers_memberships.go`'s grantMembershipRequest,
 * `web/src/memberships.ts`'s own doc comment): there is no endpoint a
 * tenant-scoped PermAdmin caller can reach that enumerates users NOT yet a
 * member of this tenant (GET /admin/users is platform-admin-only and global;
 * GET /admin/tenant-users deliberately returns only the tenant's ACTIVE
 * members, i.e. exactly the people who would already be excluded from a
 * useful "who can I invite" list; invitation HTTP endpoints do not exist yet
 * — E-ID.4). This is the same free-text fallback ADR-ACT-001 §2 established
 * for the incident assignee picker under an identical "no reachable
 * directory" gap, applied here rather than inventing a new endpoint.
 */
function GrantMembershipModal({
  orgId,
  onClose,
  onGranted,
}: {
  orgId: string;
  onClose: () => void;
  onGranted: () => void;
}) {
  const [userId, setUserId] = useState('');
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const trimmed = userId.trim();
  const canSubmit = trimmed.length > 0 && !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      await grantMembership({ org_id: orgId, user_id: trimmed });
      onGranted();
    } catch (err) {
      setProblem(err instanceof ApiError ? err.problem : { title: 'Grant failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [canSubmit, orgId, trimmed, onGranted]);

  return (
    <Modal
      visible
      header="Grant membership"
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
              Grant
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Form>
        <SpaceBetween size="m">
          {problem && (
            <div role="alert">
              <Alert type="error" header="Could not grant membership">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <FormField
            label="User ID"
            description="OneOps has no directory of users outside this tenant yet — enter the user's id directly. The server validates it and reports back if it does not exist."
          >
            <Input
              value={userId}
              onChange={({ detail }) => setUserId(detail.value)}
              disabled={busy}
              ariaLabel="User ID"
              placeholder="Required"
            />
          </FormField>
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

/**
 * Members (E-ID.2, ADR-IAC-001 extension + ADR-IAC-002): a tenant admin's
 * view of who belongs to their organisation, over the three membership
 * endpoints that already exist — list, grant, revoke — following the
 * write-action pattern ADR-ACT-001 established (confirm-before-revoke,
 * disable-while-pending, refetch-after, inline RFC 7807 errors) and the
 * accept-no-row_version-on-delete decision ADR-HARD-003 made for exactly
 * this shape of endpoint.
 *
 * ORGANIZATION ID: GET/POST /admin/memberships require an explicit org_id
 * (handlers_memberships.go:58-63, :100-108) that nothing reachable from the
 * console's own session can resolve — it is not a JWT claim, and the one
 * endpoint that could answer "which organisation is my tenant's" has no
 * route reachable below requirePlatformAdmin (see memberships.ts's doc
 * comment for the full trace). Rather than block this screen on a new Go
 * endpoint out of scope for a frontend-only story, it asks for the
 * Organization ID directly and remembers it for the tab's session
 * (`sessionStorage`, ADR-IAC-002 — the same no-persistent-client-state
 * posture `theme.ts`/`auth.ts` already use) — a tenant only has one
 * (org:tenant is 1:1, ADR-IDENTITY-001 §4), so this is a one-time-per-tab
 * setup step, not a per-screen one. A platform administrator (who can reach
 * `GET /admin/organizations`) is the realistic source of this value today.
 */
export function MembersPage() {
  const [orgIdInput, setOrgIdInput] = useState(() => readStoredOrgId());
  const [orgId, setOrgId] = useState(() => readStoredOrgId());
  const [items, setItems] = useState<MembershipDTO[]>([]);
  const [userDirectory, setUserDirectory] = useState<Record<string, TenantUserDTO>>({});
  const [loading, setLoading] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [reloads, setReloads] = useState(0);

  const [grantOpen, setGrantOpen] = useState(false);
  const [revokeTarget, setRevokeTarget] = useState<MembershipDTO | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionProblem, setActionProblem] = useState<ProblemDetail | null>(null);

  const reload = useCallback(() => setReloads((n) => n + 1), []);

  const commitOrgId = useCallback(() => {
    const trimmed = orgIdInput.trim();
    if (!trimmed) return;
    storeOrgId(trimmed);
    setOrgId(trimmed);
    setOrgIdInput(trimmed);
    reload();
  }, [orgIdInput, reload]);

  const load = useCallback(
    (signal: AbortSignal) => {
      if (!orgId) {
        setItems([]);
        setProblem(null);
        setLoading(false);
        return;
      }
      setLoading(true);
      setProblem(null);
      Promise.allSettled([
        listMemberships(orgId, MEMBERSHIP_LIST_CAP, signal),
        listTenantUsers(MEMBERSHIP_LIST_CAP, signal),
      ])
        .then(([membershipsResult, usersResult]) => {
          if (signal.aborted) return;
          if (membershipsResult.status === 'fulfilled') {
            setItems(membershipsResult.value.items ?? []);
          } else {
            const err = membershipsResult.reason;
            setProblem(
              err instanceof ApiError ? err.problem : { title: 'Could not load members', status: 0, detail: String(err) },
            );
            setItems([]);
          }
          // Name resolution degrades independently of the membership list
          // itself — a failure here (including the SAME 403 a non-admin
          // caller gets from the list call) just leaves every row showing
          // its raw user_id, per users.ts's own documented posture.
          setUserDirectory(usersResult.status === 'fulfilled' ? indexByUserId(usersResult.value.items) : {});
        })
        .finally(() => {
          if (!signal.aborted) setLoading(false);
        });
    },
    [orgId],
  );

  useEffect(() => {
    const ctrl = new AbortController();
    load(ctrl.signal);
    return () => ctrl.abort();
  }, [reloads, load]);

  const runRevoke = useCallback(async () => {
    if (!revokeTarget) return;
    setBusy(true);
    setActionProblem(null);
    try {
      await revokeMembership(revokeTarget.membership_id);
      setRevokeTarget(null);
      reload();
    } catch (err) {
      setRevokeTarget(null);
      setActionProblem(err instanceof ApiError ? err.problem : { title: 'Revoke failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [revokeTarget, reload]);

  const memberLabel = useCallback(
    (m: MembershipDTO) => {
      const u = userDirectory[m.user_id];
      return u ? `${u.display_name} (${u.email})` : m.user_id;
    },
    [userDirectory],
  );

  const columns = useMemo<TableProps.ColumnDefinition<MembershipDTO>[]>(
    () => [
      {
        id: 'user',
        header: 'User',
        isRowHeader: true,
        sortingComparator: (a, b) => memberLabel(a).localeCompare(memberLabel(b)),
        cell: (m) => (
          <div>
            <Box fontWeight="bold">{memberLabel(m)}</Box>
            {userDirectory[m.user_id] && (
              <Box fontSize="body-s" color="text-body-secondary">
                {m.user_id}
              </Box>
            )}
          </div>
        ),
      },
      {
        id: 'status',
        header: 'Status',
        sortingComparator: (a, b) => a.status.localeCompare(b.status),
        cell: (m) => <StatusIndicator type={STATUS_TYPE[m.status]}>{m.status === 'active' ? 'Active' : 'Revoked'}</StatusIndicator>,
      },
      {
        id: 'granted',
        header: 'Granted',
        sortingComparator: (a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime(),
        cell: (m) => new Date(m.created_at).toLocaleString(),
      },
      {
        id: 'updated',
        header: 'Updated',
        sortingComparator: (a, b) => new Date(a.updated_at).getTime() - new Date(b.updated_at).getTime(),
        cell: (m) => new Date(m.updated_at).toLocaleString(),
      },
      {
        id: 'actions',
        header: 'Actions',
        cell: (m) =>
          m.status === 'active' ? (
            <Button disabled={busy} onClick={() => setRevokeTarget(m)}>
              Revoke
            </Button>
          ) : (
            <Box color="text-body-secondary">—</Box>
          ),
      },
    ],
    [busy, memberLabel, userDirectory],
  );

  const forbidden = problem !== null && isForbidden(problem);

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Who belongs to your organisation. Roles are assigned by your identity provider, not here (see Identity & roles) — this screen only grants or revokes tenant membership."
        counter={orgId ? `(${items.length})` : undefined}
        actions={
          <Button onClick={() => setGrantOpen(true)} disabled={!orgId || forbidden}>
            Grant membership
          </Button>
        }
      >
        Members
      </Header>

      {actionProblem && (
        <div role="alert">
          <Alert type="error" header="Action failed" dismissible onDismiss={() => setActionProblem(null)}>
            {actionProblem.detail ?? `The server returned ${actionProblem.status}.`}
          </Alert>
        </div>
      )}

      {forbidden && <PermissionNeeded />}

      {!forbidden && (
        <Container header={<Header variant="h2">Organization</Header>}>
          <FormField
            label="Organization ID"
            description="OneOps has no endpoint yet for discovering this from your session — enter it directly (a platform administrator can look it up). Remembered for this browser tab only."
          >
            <SpaceBetween direction="horizontal" size="xs">
              <Input
                value={orgIdInput}
                onChange={({ detail }) => setOrgIdInput(detail.value)}
                ariaLabel="Organization ID"
                placeholder="Required"
              />
              <Button onClick={commitOrgId} disabled={!orgIdInput.trim()}>
                Load members
              </Button>
            </SpaceBetween>
          </FormField>
        </Container>
      )}

      {!forbidden && problem && <ErrorState problem={problem} onRetry={reload} />}

      {!forbidden && !problem && orgId && (
        <Table
          items={items}
          columnDefinitions={columns}
          trackBy="membership_id"
          loading={loading}
          loadingText="Loading members"
          variant="container"
          ariaLabels={{ tableLabel: 'Members' }}
          empty={<MembersEmpty />}
        />
      )}

      {!forbidden && !problem && !orgId && (
        <Box textAlign="center" color="inherit" padding="l">
          Enter an Organization ID above to load its members.
        </Box>
      )}

      {grantOpen && (
        <GrantMembershipModal
          orgId={orgId}
          onClose={() => setGrantOpen(false)}
          onGranted={() => {
            setGrantOpen(false);
            reload();
          }}
        />
      )}

      {revokeTarget && (
        <Modal
          visible
          header={`Revoke membership for ${memberLabel(revokeTarget)}?`}
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
                  Revoke membership
                </Button>
              </SpaceBetween>
            </Box>
          }
        >
          <Box>
            This withdraws the membership — the user immediately loses tenant access it granted. The row survives as
            &quot;Revoked&quot; rather than being deleted. Like the other config deletes, there is no optimistic-lock
            check here (ADR-HARD-003) — the confirmation above is the guard.
          </Box>
        </Modal>
      )}
    </SpaceBetween>
  );
}
