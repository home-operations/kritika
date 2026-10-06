-- A submit_review the step's output cap cut off ends the agent's run as
-- truncated, which the run's record must be allowed to say.
ALTER TABLE agent_runs DROP CONSTRAINT agent_runs_stop_reason_check;
ALTER TABLE agent_runs ADD CONSTRAINT agent_runs_stop_reason_check
    CHECK (stop_reason IN ('submitted', 'max_steps', 'budget', 'no_submit', 'truncated', 'canceled', 'error', 'skipped'));
