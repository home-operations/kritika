-- Whether the repository's own .kritika.yaml, as of the commit that its
-- last index job indexed, turns it off. Only an index job reads that file
-- from the default branch, so it records what it found here, and the
-- leader's index sweep counts such a repository as not running: its index is
-- dropped after KRITIKA_INDEX_GRACE, as for one turned off in kritika.
ALTER TABLE repositories ADD COLUMN repo_config_off boolean NOT NULL DEFAULT false;
