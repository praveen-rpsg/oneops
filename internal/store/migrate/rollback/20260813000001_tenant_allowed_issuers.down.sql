-- Reverses 20260813000001_tenant_allowed_issuers.
--
-- Restores the twelve-value administrative-operation CHECK and drops the
-- allowed_issuers column. Dropping the column removes the issuer-to-tenant
-- binding, which reopens the multi-IdP cross-tenant bypass (ADR-IDENTITY-003) —
-- so this down-migration is a genuine rollback of a security control, not a
-- cosmetic revert.

ALTER TABLE admin_audit_event DROP CONSTRAINT ck_admin_audit_operation;
ALTER TABLE admin_audit_event ADD CONSTRAINT ck_admin_audit_operation CHECK (operation IN (
    'user.created', 'user.profile_updated', 'user.status_changed',
    'organization.created', 'organization.status_changed',
    'tenant.created', 'tenant.status_changed',
    'invitation.created', 'invitation.redeemed', 'invitation.revoked',
    'membership.granted', 'membership.revoked'));

ALTER TABLE tenant DROP COLUMN IF EXISTS allowed_issuers;
