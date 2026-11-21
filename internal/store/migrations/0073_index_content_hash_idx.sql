-- Finds the chunks of a repository that hold a content hash
-- (0064_index_content_hash), as store.IndexVectors reads them. It is
-- partial: the chunks that have no hash, all of them at first, cost it
-- nothing. index_chunks may not exist yet, and the leader creates it with
-- the index (store.EnsureIndexSchema).
--
-- Building it reads every chunk under a lock that lets searches read the
-- table but holds up builds' writes until it is done. It is a transaction
-- of its own, so that no exclusive lock of 0064_index_content_hash is held
-- while it reads, and it looks for the index first, so that it takes no
-- lock when the index is there. A wait for the lock gives up after a few
-- seconds; the leader tries the migration again a retry interval later
-- (KRITIKA_LEADER_RETRY_INTERVAL).
SET LOCAL lock_timeout = '5s';
DO $$
BEGIN
    IF to_regclass('index_chunks') IS NOT NULL AND to_regclass('index_chunks_content_hash_idx') IS NULL THEN
        CREATE INDEX index_chunks_content_hash_idx ON index_chunks (repository_id, content_hash)
            WHERE content_hash IS NOT NULL;
    END IF;
END $$;
