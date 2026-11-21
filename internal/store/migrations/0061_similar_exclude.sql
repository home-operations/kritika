-- The paths a run's similar-code searches leave out: the change's files, by
-- both names of a rename. The runner reports them once, before its first
-- search, and each search reads them here. A search request then stays the
-- same size however many files the change touches. NULL until reported.
--
-- Adding the column takes the exclusive lock of runner_runs, which a wait
-- for it would hold up every run's reads behind. So the wait gives up after
-- a few seconds, the migration fails, and the leader tries it again a retry
-- interval later (KRITIKA_LEADER_RETRY_INTERVAL).
SET LOCAL lock_timeout = '5s';
ALTER TABLE runner_runs ADD COLUMN IF NOT EXISTS similar_exclude text[];
