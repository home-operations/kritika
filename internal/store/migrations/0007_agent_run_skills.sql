-- The repository skills a review's agent was offered, and the ones it
-- opened, which the review's summary states.
ALTER TABLE agent_runs ADD COLUMN skills_offered text[] NOT NULL DEFAULT '{}';
ALTER TABLE agent_runs ADD COLUMN skills_opened text[] NOT NULL DEFAULT '{}';
