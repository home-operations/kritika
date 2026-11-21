-- A window of a declaration that does not show the line naming the
-- declaration carries that line, its signature, which the worker embeds
-- above the window's text. Every other chunk has none, and so does every
-- chunk that a gateway of a build before the column stages.
--
-- Adding the column takes the exclusive lock of index_staging, which a wait
-- for it would hold up every runner's staging behind. So the wait gives up
-- after a few seconds, the migration fails, and the leader tries it again a
-- retry interval later (KRITIKA_LEADER_RETRY_INTERVAL).
SET LOCAL lock_timeout = '5s';
ALTER TABLE index_staging ADD COLUMN IF NOT EXISTS signature text NOT NULL DEFAULT '';
