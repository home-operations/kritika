ALTER TABLE usage ADD COLUMN unpriced boolean NOT NULL DEFAULT false;
ALTER TABLE usage ADD CONSTRAINT usage_unpriced_cost CHECK (NOT unpriced OR (cost_usd = 0 AND NOT chatgpt_plan));

ALTER TABLE model_calls ADD COLUMN unpriced boolean NOT NULL DEFAULT false;
ALTER TABLE model_calls ADD CONSTRAINT model_calls_unpriced_cost CHECK (NOT unpriced OR (cost_usd = 0 AND NOT chatgpt_plan));

-- The agent's steps alone, which cost_usd sums, not the run's other calls.
ALTER TABLE agent_runs ADD COLUMN unpriced_steps integer NOT NULL DEFAULT 0;
