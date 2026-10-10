ALTER TABLE usage ADD COLUMN chatgpt_plan boolean NOT NULL DEFAULT false;
ALTER TABLE usage ADD CONSTRAINT usage_plan_cost CHECK (NOT chatgpt_plan OR cost_usd = 0);

ALTER TABLE model_calls ADD COLUMN chatgpt_plan boolean NOT NULL DEFAULT false;
ALTER TABLE model_calls ADD CONSTRAINT model_calls_plan_cost CHECK (NOT chatgpt_plan OR cost_usd = 0);
