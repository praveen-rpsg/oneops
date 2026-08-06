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
import Spinner from '@cloudscape-design/components/spinner';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import type { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import type { TableProps } from '@cloudscape-design/components/table';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { ErrorState } from '../components/States';
import {
  MEMBERSHIP_LIST_CAP,
  getTenantOrg,
  grantMembership,
  listMemberships,
  revokeMembership,
} from '../memberships';
import type { MembershipDTO, OrganizationDTO } from '../memberships';
import { listTenantUsers } from '../users';
import type { TenantUserDTO } from '../users';

/**
 * Remembers a MANUALLY ENTERED organisation id, for this TAB's session
 * only — `sessionStorage`, not `localStorage`, matching the
 * no-persistent-client-state posture `theme.ts`/`auth.ts` already establish
 * for this console (the access token itself is memory-only; the theme
 * override is `sessionStorage`).
 *
 * FALLBACK ONLY (E-ID.2a): `GET /v1/admin/tenant-org` now resolves the
 * caller's own org_id automatically — see `MembersPage`'s own doc comment.
 * This manual path only runs when that call fails for a reason other than
 * "not an admin" (network/server error), so the screen still works rather
 * than hard-blocking on the resolver being unavailable.
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
 * Members (E-ID.2/E-ID.2a, ADR-IAC-001 extension + ADR-IAC-002): a tenant
 * admin's view of who belongs to their organisation, over the membership
 * endpoints that already exist — list, grant, revoke — following the
 * write-action pattern ADR-ACT-001 established (confirm-before-revoke,
 * disable-while-pending, refetch-after, inline RFC 7807 errors) and the
 * accept-no-row_version-on-delete decision ADR-HARD-003 made for exactly
 * this shape of endpoint.
 *
 * ORGANIZATION ID (E-ID.2a — auto-resolved, previously manual): GET/POST
 * /admin/memberships require an explicit org_id (handlers_memberships.go:
 * 58-63, :100-108). This screen resolves it AUTOMATICALLY on mount via
 * `GET /v1/admin/tenant-org` (`getTenantOrg`, `handlers_organizations.go:186`)
 * — the caller's own organisation, from the authenticated context, no input
 * needed — and shows it as read-only context ("Organization: {name}"), never
 * an editable field. Manual entry (`sessionStorage`-remembered, ADR-IAC-002)
 * is kept ONLY as a fallback for when that call fails for a reason other
 * than "the caller isn't an admin": a 403 from `tenant-org` means exactly
 * what a 403 from the membership endpoints would mean (not a tenant admin),
 * so it goes straight to `PermissionNeeded` rather than offering an input
 * that would 403 anyway; any OTHER failure (network, 5xx) falls back to the
 * manual path so the screen still works rather than hard-failing on the
 * resolver being unavailable.
 */
export function MembersPage() {
  const [orgResolution, setOrgResolution] = useState<
    | { kind: 'resolving' }
    | { kind: 'resolved'; org: OrganizationDTO }
    | { kind: 'forbidden' }
    | { kind: 'manual' }
  >({ kind: 'resolving' });

  // Manual-entry fallback state only — see the doc comment above and on
  // ORG_ID_STORAGE_KEY. Untouched on the happy (resolved) path.
  const [manualOrgIdInput, setManualOrgIdInput] = useState(() => readStoredOrgId());
  const [manualOrgId, setManualOrgId] = useState(() => readStoredOrgId());

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

  // Resolve the caller's own organisation once, on mount. A 403 means "not
  // an admin" (the same refusal the membership endpoints themselves would
  // give) — surfaced as the permission-needed state, not the manual
  // fallback, since typing an id would not help. Any other failure falls
  // back to manual entry.
  useEffect(() => {
    const ctrl = new AbortController();
    getTenantOrg(ctrl.signal)
      .then((org) => {
        if (ctrl.signal.aborted) return;
        setOrgResolution({ kind: 'resolved', org });
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        if (err instanceof ApiError && err.problem.status === 403) {
          setOrgResolution({ kind: 'forbidden' });
        } else {
          setOrgResolution({ kind: 'manual' });
        }
      });
    return () => ctrl.abort();
  }, []);

  const orgId =
    orgResolution.kind === 'resolved' ? orgResolution.org.org_id : orgResolution.kind === 'manual' ? manualOrgId : '';

  const commitManualOrgId = useCallback(() => {
    const trimmed = manualOrgIdInput.trim();
    if (!trimmed) return;
    storeOrgId(trimmed);
    setManualOrgId(trimmed);
    setManualOrgIdInput(trimmed);
    reload();
  }, [manualOrgIdInput, reload]);

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

  // A 403 can come from either the resolver or (defensively — both use the
  // same PermAdmin tier, so this should not diverge in practice) the
  // membership list call itself.
  const forbidden = orgResolution.kind === 'forbidden' || (problem !== null && isForbidden(problem));
  const resolving = orgResolution.kind === 'resolving';

  return (
    <SpaceBetween size="l">
      <Header
        variant="h1"
        description="Who belongs to your organisation. Roles are assigned by your identity provider, not here (see Identity & roles) — this screen only grants or revokes tenant membership."
        counter={orgId ? `(${items.length})` : undefined}
        actions={
          <Button onClick={() => setGrantOpen(true)} disabled={!orgId || forbidden || resolving}>
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

      {resolving && (
        <Box textAlign="center" color="inherit" padding="l">
          <div role="status" aria-busy="true">
            <Spinner size="normal" /> Resolving your organization…
          </div>
        </Box>
      )}

      {forbidden && <PermissionNeeded />}

      {!resolving && !forbidden && orgResolution.kind === 'resolved' && (
        <Container header={<Header variant="h2">Organization</Header>}>
          <Box fontWeight="bold">{orgResolution.org.name}</Box>
          <Box fontSize="body-s" color="text-body-secondary">
            {orgResolution.org.org_id}
          </Box>
        </Container>
      )}

      {!resolving && !forbidden && orgResolution.kind === 'manual' && (
        <Container header={<Header variant="h2">Organization</Header>}>
          <FormField
            label="Organization ID"
            description="Automatic organization lookup failed — enter it directly (a platform administrator can look it up). Remembered for this browser tab only."
          >
            <SpaceBetween direction="horizontal" size="xs">
              <Input
                value={manualOrgIdInput}
                onChange={({ detail }) => setManualOrgIdInput(detail.value)}
                ariaLabel="Organization ID"
                placeholder="Required"
              />
              <Button onClick={commitManualOrgId} disabled={!manualOrgIdInput.trim()}>
                Load members
              </Button>
            </SpaceBetween>
          </FormField>
        </Container>
      )}

      {!resolving && !forbidden && problem && <ErrorState problem={problem} onRetry={reload} />}

      {!resolving && !forbidden && !problem && orgId && (
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

      {!resolving && !forbidden && !problem && orgResolution.kind === 'manual' && !orgId && (
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
