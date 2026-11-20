-- A chunk's lexemes are the words of its path, symbol and text as package
-- lexical splits code: each identifier whole and in its parts. A similar-code
-- search matches the identifiers of a query against them, beside the vector
-- search. A chunk indexed before has none, and matches no query by name until
-- its index is rebuilt. They have no index: under row-level security, no
-- index of them can serve the search (store.lexicalChunksSQL).
-- index_chunks is created by the leader once an embedder is configured
-- (store.EnsureIndexSchema), which creates it with the column, so it may not
-- exist yet.
DO $$
BEGIN
    IF to_regclass('index_chunks') IS NOT NULL THEN
        ALTER TABLE index_chunks ADD COLUMN IF NOT EXISTS lexemes tsvector;
    END IF;
END $$;
