-- A chunk's lexemes are the words of its path, symbol and text as package
-- lexical splits code: each identifier whole and in its parts. A similar-code
-- search matches the identifiers of a query against them, beside the vector
-- search. A chunk indexed before has none, and matches no query by name until
-- its index is rebuilt. They have no index: under row-level security, no
-- index of them can serve the search (store.lexicalChunksSQL).
-- index_chunks is created by the leader once an embedder is configured
-- (store.EnsureIndexSchema), which creates it with the column, so it may not
-- exist yet.
--
-- Adding the column takes the exclusive lock of index_chunks and of each of
-- its partitions. A wait for it would hold up every search and build that
-- queues behind it, so it gives up after a few seconds instead. The
-- migration then fails, and the leader tries it again a retry interval
-- later (KRITIKA_LEADER_RETRY_INTERVAL).
SET LOCAL lock_timeout = '5s';
DO $$
BEGIN
    IF to_regclass('index_chunks') IS NOT NULL AND NOT EXISTS (SELECT 1 FROM pg_attribute
        WHERE attrelid = to_regclass('index_chunks') AND attname = 'lexemes' AND NOT attisdropped) THEN
        ALTER TABLE index_chunks ADD COLUMN lexemes tsvector;
    END IF;
END $$;
