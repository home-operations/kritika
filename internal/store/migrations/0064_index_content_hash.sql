-- A chunk's content hash identifies the vector of the text it was embedded
-- from: that text and the embedder. A build copies the vector of a chunk of
-- its repository with the same hash rather than embed the text again. A
-- chunk embedded before has none, and is embedded again once.
-- index_chunks is created by the leader once an embedder is configured
-- (store.EnsureIndexSchema), which creates it with the column, so it may
-- not exist yet. 0073_index_content_hash_idx indexes the column.
--
-- Adding the column takes the exclusive lock of index_chunks and of each of
-- its partitions, which it looks for first so that it takes none when the
-- column is there. A wait for the lock would hold up every search and build
-- behind it, so it gives up after a few seconds instead. The migration then
-- fails, and the leader tries it again a retry interval later
-- (KRITIKA_LEADER_RETRY_INTERVAL).
SET LOCAL lock_timeout = '5s';
DO $$
BEGIN
    IF to_regclass('index_chunks') IS NOT NULL AND NOT EXISTS (SELECT 1 FROM pg_attribute
        WHERE attrelid = to_regclass('index_chunks') AND attname = 'content_hash' AND NOT attisdropped) THEN
        ALTER TABLE index_chunks ADD COLUMN content_hash bytea;
    END IF;
END $$;
