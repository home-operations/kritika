-- A run's token carries how hard its model reasons, as it carries the
-- model: the gateway sets both on each step, and the runner chooses
-- neither. Empty leaves the effort to the provider.
ALTER TABLE gateway_tokens ADD COLUMN effort text NOT NULL DEFAULT '';
