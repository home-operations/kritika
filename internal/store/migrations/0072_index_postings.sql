-- The search by name's inverted index: for each word (lexeme) of each index
-- chunk, as package lexical makes them, the chunk that holds it, under the
-- chunk's generation. A search reads the chunks of each of its words, and
-- how many there are, from the primary key alone. Under row-level security
-- a B-tree serves it, as text and uuid equality are leakproof, where no
-- index of the chunks' lexemes could (the full-text match operator is not).
-- account_id is in the key for the policy, so that the search needs no row.
--
-- It is not partitioned: the key starts with the generation, so a search and
-- a generation's deletion read that generation's postings alone, as a
-- partition would. It has no foreign keys, which would cost a check of each
-- posting, and a generation's chunks hold dozens of postings each. Every
-- path that deletes or moves a generation's chunks does the same to their
-- postings, and nothing deletes an index run.
--
-- index_chunks.lexemes says which postings to delete with a chunk. A chunk
-- written by a build before this table has none, and a generation holding
-- one is not searched by name (store.ActiveGenerationFor) until the
-- onboarding feeder rebuilds it for the new index format.
SET LOCAL lock_timeout = '5s';
CREATE TABLE index_postings (
    index_run_id uuid NOT NULL,
    chunk_id     uuid NOT NULL,
    account_id   uuid NOT NULL,
    lexeme       text NOT NULL,
    PRIMARY KEY (index_run_id, lexeme, chunk_id) INCLUDE (account_id)
);
ALTER TABLE index_postings ENABLE ROW LEVEL SECURITY;
CREATE POLICY account_isolation ON index_postings
    USING      (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid)
    WITH CHECK (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid);

-- The chunks a generation holds, which each search by name weighs its words
-- against: counted as each publication leaves them, where counting them for
-- each search would read the whole generation. chunk_count stays what the
-- run itself embedded. A generation published before has none, and is not
-- searched by name.
ALTER TABLE index_runs ADD COLUMN generation_chunks int;
