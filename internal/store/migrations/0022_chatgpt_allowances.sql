ALTER TABLE chatgpt_sessions ADD COLUMN allowances jsonb NOT NULL DEFAULT '{}'::jsonb;

-- Accounting retains the run identity after its transcript is pruned.
ALTER TABLE usage ADD COLUMN runner_run_id uuid;
