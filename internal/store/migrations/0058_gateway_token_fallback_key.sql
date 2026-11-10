-- A run's token carries its fallback as configured beside the fallback it
-- was granted, which a floating alias pins to one model: a fallback step's
-- slot is keyed by the configured reference, one pool whichever model the
-- alias selects, shared with the worker's own calls on it.
ALTER TABLE gateway_tokens ADD COLUMN fallback_key text NOT NULL DEFAULT '';
