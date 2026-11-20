-- A window of a declaration that does not show the line naming the
-- declaration carries that line, its signature, which the worker embeds
-- above the window's text. Every other chunk has none, and so does every
-- chunk that a gateway of a build before the column stages.
ALTER TABLE index_staging ADD COLUMN signature text NOT NULL DEFAULT '';
