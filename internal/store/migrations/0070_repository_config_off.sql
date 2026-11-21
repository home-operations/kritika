-- Whether the repository's own .kritika.yaml, as of the commit that its
-- last index job indexed, turns it off. Only an index job reads that file
-- from the default branch, so it records what it found here, and the
-- leader's index sweep counts such a repository as not running: its index is
-- dropped after KRITIKA_INDEX_GRACE, as for one turned off in kritika.
--
-- Adding the column takes the exclusive lock of repositories, which a wait
-- for it would hold up every webhook and job behind. So the wait gives up
-- after a few seconds, the migration fails, and the leader tries it again a
-- retry interval later (KRITIKA_LEADER_RETRY_INTERVAL).
SET LOCAL lock_timeout = '5s';
ALTER TABLE repositories ADD COLUMN IF NOT EXISTS repo_config_off boolean NOT NULL DEFAULT false;
