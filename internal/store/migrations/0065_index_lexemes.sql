-- A chunk's lexemes are the words of its path, symbol and text as package
-- lexical splits code: each identifier whole and in its parts. A similar-code
-- search matches the identifiers of a query against them, beside the vector
-- search. A chunk indexed before has none, and matches no query by name until
-- its index is rebuilt.
-- index_chunks is created by the leader once an embedder is configured
-- (store.EnsureIndexSchema), which creates it with the column, so it may not
-- exist yet. The index is partial: the chunks that have no lexemes, all of
-- them at first, cost it nothing.
DO $$
BEGIN
    IF to_regclass('index_chunks') IS NOT NULL THEN
        ALTER TABLE index_chunks ADD COLUMN IF NOT EXISTS lexemes tsvector;
        CREATE INDEX IF NOT EXISTS index_chunks_lexemes_idx ON index_chunks USING gin (lexemes)
            WHERE lexemes IS NOT NULL;
    END IF;
END $$;
