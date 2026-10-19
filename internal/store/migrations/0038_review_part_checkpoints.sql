CREATE TABLE review_part_checkpoints (
    runner_run_id uuid NOT NULL REFERENCES runner_runs(id) ON DELETE CASCADE,
    account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    input_hash text NOT NULL,
    part integer NOT NULL CHECK (part > 0),
    source_run_id uuid NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (runner_run_id, input_hash, part)
);

ALTER TABLE review_part_checkpoints ENABLE ROW LEVEL SECURITY;
CREATE POLICY account_isolation ON review_part_checkpoints
    USING (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid);
CREATE POLICY runner_job ON review_part_checkpoints
    USING (runner_run_id = NULLIF(current_setting('app.runner_job_id', true), '')::uuid
           AND EXISTS (SELECT 1 FROM runner_runs r WHERE r.id = runner_run_id))
    WITH CHECK (runner_run_id = NULLIF(current_setting('app.runner_job_id', true), '')::uuid
                AND account_id = (SELECT r.account_id FROM runner_runs r WHERE r.id = runner_run_id));
