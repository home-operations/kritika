ALTER TABLE index_runs
    ADD COLUMN resume_key text NOT NULL DEFAULT '',
    ADD COLUMN embedding_ready boolean NOT NULL DEFAULT false,
    ADD COLUMN embedding_cursor bigint NOT NULL DEFAULT 0;

CREATE INDEX runner_runs_index_job_idx ON runner_runs (river_job_id) WHERE index_run_id IS NOT NULL;

CREATE INDEX index_runs_resumable_idx ON index_runs (created_at) WHERE resume_key <> '';
