-- The search by name's inverted index: for each word (lexeme) of a
-- generation's chunks, as package lexical makes them, how many of the chunks
-- hold it and, while fewer than store.LexicalCommonMax do, which. A search
-- reads its words' rows by the primary key. Under row-level security a B-tree
-- serves it, as text equality is leakproof, where no index of the chunks'
-- lexemes could (the full-text match operator is not).
--
-- A row a word, not a row a word of each chunk: code holds about 50 words a
-- chunk but a few tens of thousands of words in all, so a row of each word of
-- each chunk would take several times the space of the chunks' vectors. A
-- word held by store.LexicalCommonMax chunks or more is too common for any
-- search to read, so its row keeps the count alone. Such words are most of a
-- large generation's postings.
--
-- A full build writes its generation's rows in one statement as it
-- publishes, from the chunks' lexemes. An incremental step updates the rows
-- of the words of the chunks it replaces and adds, and lists again the chunks
-- of a word that falls back under the bar. Every other path drops a
-- generation's rows with its chunks.
--
-- It is not partitioned: the key starts with the generation, so a search and
-- a generation's deletion read that generation's rows alone, as a partition
-- would. A generation published before has none, and is not searched by name
-- (store.ActiveGenerationFor) until the onboarding feeder rebuilds it for the
-- new index format.
SET LOCAL lock_timeout = '5s';
CREATE TABLE IF NOT EXISTS index_postings (
    index_run_id uuid   NOT NULL,
    lexeme       text   NOT NULL,
    account_id   uuid   NOT NULL,
    chunks       int    NOT NULL,
    chunk_ids    uuid[],
    PRIMARY KEY (index_run_id, lexeme)
);
ALTER TABLE index_postings ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS account_isolation ON index_postings;
CREATE POLICY account_isolation ON index_postings
    USING      (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid)
    WITH CHECK (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid);

-- The chunks a generation holds, which each search by name weighs its words
-- against: counted as each publication leaves them, where counting them for
-- each search would read the whole generation. chunk_count stays what the
-- run itself embedded. A generation published before has none, and is not
-- searched by name.
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS generation_chunks int;
