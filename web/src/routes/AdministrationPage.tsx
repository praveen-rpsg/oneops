import Alert from '@cloudscape-design/components/alert';
import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Container from '@cloudscape-design/components/container';
import Header from '@cloudscape-design/components/header';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import type { TableProps } from '@cloudscape-design/components/table';
import { getRoles, getSubject } from '../auth';
import { PERMISSIONS, ROLES, ROLE_PERMISSIONS, effectivePermissions } from '../rbac';
import type { Permission, Role } from '../rbac';

const SCOPE_LABEL: Record<Permission['scope'], string> = { tenant: 'Tenant', platform: 'Platform' };

function PermissionBadges({ permissions }: { permissions: Permission[] }) {
  if (permissions.length === 0) {
    return <Box color="text-body-secondary">None.</Box>;
  }
  return (
    <SpaceBetween direction="horizontal" size="xs">
      {permissions.map((p) => (
        <Badge key={p.value} color={p.scope === 'platform' ? 'red' : 'blue'}>
          {p.label} ({SCOPE_LABEL[p.scope]})
        </Badge>
      ))}
    </SpaceBetween>
  );
}

/**
 * "Who am I": the console's own read of the current session — subject and
 * roles are OIDC token claims (auth.ts' getSubject/getRoles, DISPLAY-ONLY),
 * effective permissions are this session's roles run through rbac.ts'
 * restatement of internal/auth/rbac.go. None of this is consulted for
 * authorization; the server re-derives and enforces it independently on
 * every request.
 */
function WhoAmI() {
  const subject = getSubject();
  const roles = getRoles();

  if (!subject) {
    return (
      <Container header={<Header variant="h2">Who am I</Header>}>
        <Alert type="info">
          Running with authentication disabled (local development) — no token to introspect.
        </Alert>
      </Container>
    );
  }

  const permissions = effectivePermissions(roles);

  return (
    <Container header={<Header variant="h2">Who am I</Header>}>
      <KeyValuePairs
        columns={1}
        items={[
          { label: 'Subject', value: subject },
          {
            label: 'Roles',
            value:
              roles.length > 0 ? (
                <SpaceBetween direction="horizontal" size="xs">
                  {roles.map((r) => (
                    <Badge key={r}>{r}</Badge>
                  ))}
                </SpaceBetween>
              ) : (
                <Box color="text-body-secondary">No roles claimed by this token.</Box>
              ),
          },
          { label: 'Effective permissions', value: <PermissionBadges permissions={permissions} /> },
        ]}
      />
    </Container>
  );
}

/**
 * "Roles & permissions": a static reference table restating rbac.ts (which
 * itself restates internal/auth/rbac.go) — one row per role, one column per
 * permission. This is documentation, not a store: there is no "Role" or
 * "Permission" entity behind it, and nothing here can be edited — roles are
 * assigned by the identity provider (OIDC), never by OneOps.
 */
function RolesMatrix() {
  const permissionList = Object.values(PERMISSIONS);

  const columns: TableProps.ColumnDefinition<Role>[] = [
    { id: 'role', header: 'Role', isRowHeader: true, cell: (role) => role },
    ...permissionList.map((perm) => ({
      id: perm.value,
      header: `${perm.label} (${SCOPE_LABEL[perm.scope]})`,
      cell: (role: Role) =>
        ROLE_PERMISSIONS[role].some((p) => p.value === perm.value) ? (
          <StatusIndicator type="success">Yes</StatusIndicator>
        ) : (
          <Box color="text-body-secondary" textAlign="center">
            —
          </Box>
        ),
    })),
  ];

  return (
    <Container header={<Header variant="h2">Roles &amp; permissions</Header>}>
      <SpaceBetween size="m">
        <Alert type="info">
          Roles are assigned by your identity provider (OIDC). OneOps enforces them on every request but does not
          create, assign, or edit roles here.
        </Alert>
        <Table
          items={[...ROLES]}
          columnDefinitions={columns}
          trackBy={(role) => role}
          variant="embedded"
          ariaLabels={{ tableLabel: 'OneOps roles and the permissions each one grants' }}
        />
      </SpaceBetween>
    </Container>
  );
}

/**
 * The Identity & Access console's foundation screen (E-ID.1). OneOps is a
 * relying party: it surfaces identity and roles the IdP already asserted, it
 * does not own credentials or a role store of its own — see WhoAmI/RolesMatrix.
 */
export function AdministrationPage() {
  return (
    <SpaceBetween size="l">
      <Header variant="h1" description="Your identity and OneOps' role-based access model.">
        Administration
      </Header>
      <WhoAmI />
      <RolesMatrix />
    </SpaceBetween>
  );
}
