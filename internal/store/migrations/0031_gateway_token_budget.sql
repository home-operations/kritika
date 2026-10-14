-- A token with no budget admits its run to the egress proxy and nothing
-- else: an index run's, whose runner fetches through the gateway but calls
-- no model.
ALTER TABLE gateway_tokens DROP CONSTRAINT gateway_tokens_budget_tokens_check;
ALTER TABLE gateway_tokens ADD CONSTRAINT gateway_tokens_budget_tokens_check CHECK (budget_tokens >= 0);
