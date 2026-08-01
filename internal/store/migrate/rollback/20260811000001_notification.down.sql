-- Reverses 20260811000001_notification.
--
-- Dropping the table drops its row-level-security policy with it, so no
-- separate policy reversal is needed (mirroring 20260810000001's rollback).

DROP TABLE IF EXISTS notification;
