-- OPS-S047b Issuer-to-tenant binding (ADR-IDENTITY-003).
--
-- Multi-IdP verification (OPS-S047a) accepts a token from ANY configured IdP
-- for ANY tenant the token claims: Verify() trusts a genuinely-signed token
-- from idp-B, and the middleware resolves the claimed tenant purely by its
-- external_id, with no check that idp-B is authorised to authenticate that
-- tenant. An actor controlling one additional IdP could therefore mint a valid
-- token asserting another tenant's external id and be admitted into that
-- tenant's row-level-security boundary — a cross-tenant authorization bypass.
--
-- allowed_issuers binds each tenant to the issuer(s) permitted to authenticate
-- it. SAFE BY DEFAULT AND BACKWARD COMPATIBLE: an EMPTY set means the tenant may
-- authenticate ONLY via the default IdP (ONEOPS_JWT_ISSUER). Existing tenants
-- (empty) keep working unchanged with the default IdP; a tenant that wants an
-- additional IdP must explicitly list that IdP's issuer. Enforcement is in
-- resolveTenant (internal/httpapi/middleware.go), fail-closed: a token whose
-- verified issuer is not allowed for the claimed tenant is rejected (403), never
-- admitted.
--
-- text[] rather than a child table: the set is tiny (one or two issuers per
-- tenant), read on every authenticated request, and never queried by member.
-- NOT NULL DEFAULT '{}' makes "no explicit binding" a first-class value, which
-- is exactly the empty-means-default rule above. `tenant` is the registry, not a
-- tenant-owned table (excluded from TenantOwnedTables by design), so this add
-- needs no row-level-security policy.

ALTER TABLE tenant
    ADD COLUMN IF NOT EXISTS allowed_issuers text[] NOT NULL DEFAULT '{}';

COMMENT ON COLUMN tenant.allowed_issuers IS
    'Issuers (JWT iss) permitted to authenticate this tenant. EMPTY means only the default IdP (ONEOPS_JWT_ISSUER) may authenticate it (safe-by-default, backward compatible). Enforced fail-closed in resolveTenant (ADR-IDENTITY-003).';

-- A change to a tenant's allowed_issuers is an identity-governance fact and is
-- recorded on the administrative audit chain (ADR-AUDIT-007 §6.13: a new
-- administrative operation is a new value here and needs no change to the ADR).
-- Adding the value to the disjoint dotted vocabulary keeps the domain's
-- AdminOperation.Valid pre-check and the database CHECK in agreement.
ALTER TABLE admin_audit_event DROP CONSTRAINT ck_admin_audit_operation;
ALTER TABLE admin_audit_event ADD CONSTRAINT ck_admin_audit_operation CHECK (operation IN (
    'user.created', 'user.profile_updated', 'user.status_changed',
    'organization.created', 'organization.status_changed',
    'tenant.created', 'tenant.status_changed', 'tenant.issuers_changed',
    'invitation.created', 'invitation.redeemed', 'invitation.revoked',
    'membership.granted', 'membership.revoked'));
