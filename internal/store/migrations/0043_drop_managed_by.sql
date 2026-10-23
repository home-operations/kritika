-- The configuration no longer lists repositories: every row is the forge's
-- to report, and the dashboard's to turn on.
ALTER TABLE repositories DROP COLUMN managed_by;
