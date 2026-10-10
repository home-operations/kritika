ALTER TABLE usage ADD COLUMN unpriced boolean NOT NULL DEFAULT false;
ALTER TABLE usage ADD CONSTRAINT usage_unpriced_cost CHECK (NOT unpriced OR (cost_usd = 0 AND NOT chatgpt_plan));
CREATE INDEX usage_unpriced_run_idx ON usage (runner_run_id) WHERE unpriced AND role = 'review';

ALTER TABLE model_calls ADD COLUMN unpriced boolean NOT NULL DEFAULT false;
ALTER TABLE model_calls ADD CONSTRAINT model_calls_unpriced_cost CHECK (NOT unpriced OR (cost_usd = 0 AND NOT chatgpt_plan));
