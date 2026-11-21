-- Finds the lowest and the highest index format (0062_index_format) of the
-- completed steps that advanced a generation, which store's check of
-- whether a step of another format advanced it reads. A search asks it each
-- time it runs, and without the index it reads the repository's every run,
-- which grow with each push.
--
-- Building it reads every run under a lock that lets searches read the
-- table but holds up index jobs' writes until it is done. It is a
-- transaction of its own, so that no exclusive lock of 0062_index_format is
-- held while it reads, and it looks for the index first, so that it takes no
-- lock when the index is there. A wait for the lock gives up after a few
-- seconds; the leader tries the migration again a retry interval later
-- (KRITIKA_LEADER_RETRY_INTERVAL).
SET LOCAL lock_timeout = '5s';
DO $$
BEGIN
    IF to_regclass('index_runs_stepped_idx') IS NULL THEN
        CREATE INDEX index_runs_stepped_idx ON index_runs (stepped_run_id, index_format)
            WHERE status = 'completed' AND stepped_run_id IS NOT NULL;
    END IF;
END $$;
