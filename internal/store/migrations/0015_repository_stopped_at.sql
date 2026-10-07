-- When a repository that holds an index was first found not running, for
-- any reason: disabled, turned off, archived, off in the configuration or
-- under an account no App serves. The leader's sweep sets it, clears it
-- once the repository runs again, and drops the index once it is older
-- than KRITIKA_INDEX_GRACE. A repository already disabled or turned off
-- keeps the time it went off, so its grace does not start again.
ALTER TABLE repositories ADD COLUMN stopped_at timestamptz;
UPDATE repositories SET stopped_at = least(
    CASE WHEN NOT enabled THEN disabled_at END,
    CASE WHEN turned_on = false THEN turned_at END)
WHERE active_index_run_id IS NOT NULL;
