-- Reverses 20260810000001_team.
--
-- Dropping the tables drops their row-level-security policies with them, so no
-- separate policy reversal is needed (mirroring 20260804000004's rollback).
-- team_membership references team, so it is dropped first.

DROP TABLE IF EXISTS team_membership;
DROP TABLE IF EXISTS team;
