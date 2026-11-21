-- The paths a run's similar-code searches leave out: the change's files, by
-- both names of a rename. The runner reports them once, before its first
-- search, and each search leaves them out by the table's index, a probe per
-- chunk it considers. So neither a search request nor its statements carry
-- them, however many files the change touches. A Git path can be longer than
-- a B-tree entry, which a default 8 KiB page holds a little under 2.7 KiB
-- of, so the paths' index is a hash index, which holds a hash of a path of
-- any length, and a probe compares the path itself. A probe finds the path's
-- rows of every run that reported it, and keeps its own run's; a run's rows
-- go once it has finished, so those are the few runs that are live at once.
-- No digest of the path stands in: Postgres that allows only FIPS-approved
-- algorithms refuses md5(), and the text-to-bytes conversion that sha256()
-- needs is not leakproof, so under the application role's row-level
-- security a probe could not use an index of it. The run's index serves the
-- sweep of a finished run's rows.
-- The run's similar_exclude_reported_at says that it reported them, so that
-- its first report holds even when it holds no path. The leader deletes a
-- run's rows once the run has finished, when no search can read them.
--
-- The table's foreign key, and the column, take locks of runner_runs that a
-- wait for them would hold up every run's writes or reads behind. So the
-- wait gives up after a few seconds, the migration fails, and the leader
-- tries it again a retry interval later (KRITIKA_LEADER_RETRY_INTERVAL).
--
-- runner_runs is locked first, in the mode the column takes, while the
-- migration holds no lock that a run's transaction waits for. A run that
-- read its row (WithRunner) and then writes it, while the migration waits,
-- goes ahead of the wait. Had the foreign key taken its weaker lock first,
-- which stops writes, that write would wait for the migration while the
-- migration waits for the run: a deadlock that aborts the run. accounts,
-- which the other foreign key locks against writes, is taken without a
-- wait, or the migration fails, as a wait for it would hold runner_runs.
SET LOCAL lock_timeout = '5s';
LOCK TABLE runner_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE accounts IN SHARE ROW EXCLUSIVE MODE NOWAIT;

ALTER TABLE runner_runs ADD COLUMN IF NOT EXISTS similar_exclude_reported_at timestamptz;

CREATE TABLE IF NOT EXISTS similar_exclusions (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    runner_run_id uuid NOT NULL REFERENCES runner_runs (id) ON DELETE CASCADE,
    account_id    uuid NOT NULL REFERENCES accounts (id),
    path          text NOT NULL
);
CREATE INDEX IF NOT EXISTS similar_exclusions_run_idx ON similar_exclusions (runner_run_id);
CREATE INDEX IF NOT EXISTS similar_exclusions_path_idx ON similar_exclusions USING hash (path);

-- A row is its run's account's, which only that account's run can write.
ALTER TABLE similar_exclusions ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS account_isolation ON similar_exclusions;
CREATE POLICY account_isolation ON similar_exclusions
    USING      (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid)
    WITH CHECK (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid
                AND EXISTS (SELECT 1 FROM runner_runs r
                            WHERE r.id = similar_exclusions.runner_run_id AND r.account_id = similar_exclusions.account_id));
