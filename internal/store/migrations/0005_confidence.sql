-- A review's confidence: the score a second model gave the reviewed pull
-- request, the threshold it was held to, its reason and the model that gave
-- it. NULL where the repository asks for none, or the scorer did not answer.
ALTER TABLE reviews ADD COLUMN confidence jsonb;

-- The scorer's call is charged and recorded as its own.
ALTER TABLE usage DROP CONSTRAINT usage_role_check;
ALTER TABLE usage ADD CONSTRAINT usage_role_check
    CHECK (role IN ('review', 'embedding', 'followup', 'confidence'));
ALTER TABLE model_calls DROP CONSTRAINT model_calls_kind_check;
ALTER TABLE model_calls ADD CONSTRAINT model_calls_kind_check
    CHECK (kind IN ('agent_step', 'followup', 'confidence'));
