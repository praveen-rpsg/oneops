-- Reverses 20260816000001_asset.
--
-- Dropping the tables drops their row-level-security policies with them, so
-- no separate policy reversal is needed (mirroring 20260810000001's
-- rollback). asset_relationship references asset, so it is dropped first.

DROP TABLE IF EXISTS asset_relationship;
DROP TABLE IF EXISTS asset;
