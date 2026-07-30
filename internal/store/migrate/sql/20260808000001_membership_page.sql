-- OPS-S031 Membership management — the ordering key for the list operation.
--
-- Additive. No completed migration is altered, and no grant is issued:
-- membership is tenant-owned and already carries row-level security, so the
-- request-path role holds exactly what it needs and the policy confines it.
--
-- ix_membership_org is (org_id, status) and serves filtering, not ordering. The
-- list operation pages by membership_id — a ULID, and therefore ordered by
-- creation — so without this the planner filters by organisation and then sorts
-- that organisation's whole membership on every page. Keyset pagination over a
-- unique key is deterministic; this makes it an index walk as well.
CREATE INDEX IF NOT EXISTS ix_membership_org_page
    ON membership (org_id, membership_id);

COMMENT ON INDEX ix_membership_org_page IS
    'Ordering key for OPS-S031 keyset pagination. membership_id is a ULID and '
    'unique, so a cursor over it can neither skip a row nor repeat one.';
