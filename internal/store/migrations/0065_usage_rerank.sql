-- A reranker's call, which reorders a similar-code search's candidates, is
-- charged and recorded as its own when its provider reports what it spent.
--
-- The new check admits every row the old one did, so it is added without
-- reading the table, under the exclusive lock that replacing it takes for a
-- moment, and 0068_usage_rerank_validate checks the rows that are there
-- without that lock. A wait for the lock gives up after a few seconds rather
-- than hold up every model call's usage behind it; the leader tries the
-- migration again a retry interval later.
SET LOCAL lock_timeout = '5s';
ALTER TABLE usage DROP CONSTRAINT usage_role_check;
ALTER TABLE usage ADD CONSTRAINT usage_role_check
    CHECK (role IN ('review', 'embedding', 'followup', 'confidence', 'rerank')) NOT VALID;
