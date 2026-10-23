-- A review may run several agents beside the default pass, each an agent
-- of its own. A finding names the agents that raised it, none being the
-- default pass's; a run's token grants each agent's models, by name; and
-- a model call names the agent it was for, '' being the default pass's.
-- Each column's default is a constant, so adding it rewrites no row.
ALTER TABLE findings ADD COLUMN agents text[] NOT NULL DEFAULT '{}';
ALTER TABLE gateway_tokens ADD COLUMN agents jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(agents) = 'object');
ALTER TABLE model_calls ADD COLUMN agent text NOT NULL DEFAULT '';
