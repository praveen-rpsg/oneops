-- Reverses 20260814000001_approval_record.
--
-- Dropping the table drops its row-level-security policy with it, so no
-- separate policy reversal is needed (mirroring 20260812000001's rollback).

DROP TABLE IF EXISTS approval_record;
