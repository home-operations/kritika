-- A review and its runner run are looked up by the River job that made
-- them: a review starting carries the job's earlier attempt's part
-- checkpoints, a publication job picks up its checkpoint, and the leader
-- rescues the reviews of a job whose replica died. The indexes that held
-- the column were partial, on publication rows and unfinished runs, which
-- an earlier attempt's finished run is neither, so each lookup read every
-- run of the account, or every review in the instance. One index each over
-- every row with a job serves them all, and the partial ones it subsumes.
CREATE INDEX reviews_river_job_idx ON reviews (river_job_id) WHERE river_job_id IS NOT NULL;
CREATE INDEX runner_runs_river_job_idx ON runner_runs (river_job_id) WHERE river_job_id IS NOT NULL;
DROP INDEX reviews_publication_job_idx;
DROP INDEX reviews_publication_cancel_job_idx;
DROP INDEX runner_runs_index_job_idx;

-- The open pull requests are a small share of a repository's, which the
-- poll lists per repository, and of an account's, which the dashboard
-- counts for attention and lists by default; closed ones only accrue.
CREATE INDEX pull_requests_open_idx ON pull_requests (repository_id, number) WHERE state = 'open';
CREATE INDEX pull_requests_account_open_idx ON pull_requests (account_id, updated_at DESC, id DESC) WHERE state = 'open';
-- The analytics' median time to merge, over the pull requests merged in a
-- window.
CREATE INDEX pull_requests_account_merged_idx ON pull_requests (account_id, closed_at) WHERE merged;

-- A settled model invocation is swept once its settlement is older than
-- the transcript retention.
CREATE INDEX model_invocations_settled_idx ON model_invocations (settled_at) WHERE state = 'settled';

-- The dashboard's queue lists an account's finished jobs newest first, by
-- the account in each job's args, which River's GIN index on args cannot
-- serve for a path lookup, and River's own indexes lead with the state.
CREATE INDEX river_job_account_finished_idx ON river_job ((args->>'account_id'), finalized_at DESC NULLS LAST, id DESC);

-- agent_runs_account_created_idx leads with the account.
DROP INDEX agent_runs_account_id_idx;
