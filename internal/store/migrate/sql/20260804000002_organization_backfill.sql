-- Every existing tenant gets its organisation (ADR-IDENTITY-002 §5, M2).
--
-- Without this, tenants created before the identity model would have no
-- organisation, and the 1:1 that ADR-IDENTITY-001 §4 asserts would hold only for
-- rows created after it. An invariant that is true only of new data is not an
-- invariant, and OrganizationTenantValidator would fail closed at startup on the
-- first deployment carrying real tenants.
--
-- The system tenant, seeded by 20260727000001, is backfilled like any other. It
-- gets no special case here precisely so that nothing downstream needs a branch
-- for it — a special case in the data becomes a conditional in every consumer.
--
-- slug and name are copied verbatim rather than derived. ck_org_slug is the same
-- expression as ck_tenant_slug, so a slug that satisfied one satisfies the other,
-- and uq_org_slug can only collide where uq_tenant_slug already did — which it
-- cannot.
--
-- ON CONFLICT DO NOTHING makes this re-runnable. Migrations are applied once by
-- version, but a partial restore, a replayed migration directory, or an operator
-- running it by hand must not fail on rows that are already correct.

INSERT INTO organization (org_id, tenant_id, slug, name)
SELECT 'org_' || t.tenant_id, t.tenant_id, t.slug, t.name
FROM tenant t
ON CONFLICT DO NOTHING;
