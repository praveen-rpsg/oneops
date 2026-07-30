-- Operational rollback for 20260808000001_membership_page.
-- Kept out of the Atlas migration directory (which holds forward files only).
--
-- Safe and unguarded: an index carries no data. Dropping it degrades the list
-- operation to a sort per page, not to incorrect answers.
DROP INDEX IF EXISTS ix_membership_org_page;
