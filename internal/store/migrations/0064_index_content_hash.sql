-- A chunk's content hash identifies the vector of the text it was embedded
-- from: that text and the embedder. A build copies the vector of a chunk of
-- its repository with the same hash rather than embed the text again. A
-- chunk embedded before has none, and is embedded again once.
-- index_chunks is created by the leader once an embedder is configured
-- (store.EnsureIndexSchema), which creates it with the column, so it may
-- not exist yet. The index is partial: the chunks that have no hash, all of
-- them at first, cost it nothing.
DO $$
BEGIN
    IF to_regclass('index_chunks') IS NOT NULL THEN
        ALTER TABLE index_chunks ADD COLUMN IF NOT EXISTS content_hash bytea;
        CREATE INDEX IF NOT EXISTS index_chunks_content_hash_idx ON index_chunks (repository_id, content_hash)
            WHERE content_hash IS NOT NULL;
    END IF;
END $$;
