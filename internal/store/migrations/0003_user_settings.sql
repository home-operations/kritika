-- What a user chose for their own dashboard: {"timeZone", "clock",
-- "theme"}, each absent or "" while the browser decides. One document, not
-- a column each, so a new choice is no new migration.
ALTER TABLE users ADD COLUMN settings jsonb NOT NULL DEFAULT '{}'::jsonb;
