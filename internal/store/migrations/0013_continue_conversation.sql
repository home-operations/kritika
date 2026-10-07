-- A review may carry on the last review's conversation. Its run token
-- names the run whose conversation the gateway serves it, and the session
-- that conversation's steps were sent as, which its own steps keep so a
-- provider that routes by session finds its cache. A kept conversation
-- records the model reference its run was granted, which the next run
-- must share; a provider may answer under another name, such as an alias
-- resolved. An agent run records the conversation it carried on.
ALTER TABLE gateway_tokens ADD COLUMN continues uuid REFERENCES runner_runs (id);
ALTER TABLE gateway_tokens ADD COLUMN session text NOT NULL DEFAULT '';
ALTER TABLE agent_conversations ADD COLUMN model text NOT NULL DEFAULT '';
ALTER TABLE agent_runs ADD COLUMN continued_from uuid REFERENCES runner_runs (id);
