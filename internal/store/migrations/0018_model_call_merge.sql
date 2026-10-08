-- The call that writes a split review's summary from its parts' is
-- recorded as its own.
ALTER TABLE model_calls DROP CONSTRAINT model_calls_kind_check;
ALTER TABLE model_calls ADD CONSTRAINT model_calls_kind_check
    CHECK (kind IN ('agent_step', 'followup', 'confidence', 'merge'));
