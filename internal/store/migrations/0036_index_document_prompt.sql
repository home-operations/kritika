-- The text the embedder put in front of every chunk, part of which
-- embedder built an index along with its model and dimension: a change of
-- it rebuilds the index rather than mix vectors embedded two ways. Empty
-- is no prompt, which every index built before had.
ALTER TABLE index_schema ADD COLUMN document_prompt text NOT NULL DEFAULT '';
ALTER TABLE index_runs ADD COLUMN document_prompt text NOT NULL DEFAULT '';
