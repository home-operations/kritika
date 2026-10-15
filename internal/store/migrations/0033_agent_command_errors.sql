-- Command setup failures remain visible even when the agent omits them
-- from its submitted review.
ALTER TABLE agent_runs ADD COLUMN command_errors text[] NOT NULL DEFAULT '{}';
