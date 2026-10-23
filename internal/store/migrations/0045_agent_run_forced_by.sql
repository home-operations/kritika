-- The limit that made the loop tell an agent to submit with what it had:
-- its step cap, its token budget or its time running out; '' when it was
-- never told. A review submitted so may not cover all it meant to read,
-- which its summary says. Its default is a constant, so adding it rewrites
-- no row.
ALTER TABLE agent_runs ADD COLUMN forced_by text NOT NULL DEFAULT ''
    CHECK (forced_by IN ('', 'steps', 'tokens', 'time'));
