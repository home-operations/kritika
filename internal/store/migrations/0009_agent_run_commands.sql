-- The commands a review's agent was offered through the run tool, and the
-- ones it ran, which the review's summary states beside its skills.
ALTER TABLE agent_runs ADD COLUMN commands_offered text[] NOT NULL DEFAULT '{}';
ALTER TABLE agent_runs ADD COLUMN commands_run text[] NOT NULL DEFAULT '{}';
