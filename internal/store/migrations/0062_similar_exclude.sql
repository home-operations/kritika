-- The paths a run's similar-code searches leave out: the change's files, by
-- both names of a rename. The runner reports them once, before its first
-- search, and each search reads them here. A search request then stays the
-- same size however many files the change touches. NULL until reported.
ALTER TABLE runner_runs ADD COLUMN similar_exclude text[];
