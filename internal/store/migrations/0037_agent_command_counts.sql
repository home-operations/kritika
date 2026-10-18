-- NULL distinguishes runs without execution counts from measured runs
-- that started no commands. Counts cannot be reconstructed from commands_run.
ALTER TABLE agent_runs ADD COLUMN command_counts jsonb
    CHECK (jsonb_typeof(command_counts) = 'object');

CREATE INDEX agent_runs_account_created_idx ON agent_runs (account_id, created_at);
