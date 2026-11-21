-- The paths a run's similar-code searches leave out: the change's files, by
-- both names of a rename. The runner reports them once, before its first
-- search, and each search leaves them out by the table's key, a probe per
-- chunk it considers. So neither a search request nor its statements carry
-- them, however many files the change touches. The run's
-- similar_exclude_reported_at says that it reported them, so that its first
-- report holds even when it holds no path. The leader deletes a run's rows
-- once the run has finished, when no search can read them.
--
-- The table's foreign key, and the column, take locks of runner_runs that a
-- wait for them would hold up every run's writes or reads behind. So the
-- wait gives up after a few seconds, the migration fails, and the leader
-- tries it again a retry interval later (KRITIKA_LEADER_RETRY_INTERVAL).
SET LOCAL lock_timeout = '5s';

CREATE TABLE IF NOT EXISTS similar_exclusions (
    runner_run_id uuid NOT NULL REFERENCES runner_runs (id) ON DELETE CASCADE,
    account_id    uuid NOT NULL REFERENCES accounts (id),
    path          text NOT NULL,
    PRIMARY KEY (runner_run_id, path)
);

-- A row is its run's account's, which only that account's run can write.
ALTER TABLE similar_exclusions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS account_isolation ON similar_exclusions;
CREATE POLICY account_isolation ON similar_exclusions
    USING      (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid)
    WITH CHECK (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid
                AND EXISTS (SELECT 1 FROM runner_runs r
                            WHERE r.id = similar_exclusions.runner_run_id AND r.account_id = similar_exclusions.account_id));

ALTER TABLE runner_runs ADD COLUMN IF NOT EXISTS similar_exclude_reported_at timestamptz;
