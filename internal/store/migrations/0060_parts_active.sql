-- A split review gives a slot back to a review that waits for its first
-- one, once no part runs in it. The runner reports the parts that run, by
-- model, and the row keeps the slots that the runner read with that report:
-- together they bound the slots its parts can be in until it reports again.
-- The reports are numbered, so one that arrives after a later one is refused
-- and cannot put that bound below the parts that run.
ALTER TABLE runner_runs
    ADD COLUMN parts_active jsonb CHECK (jsonb_typeof(parts_active) = 'object'),
    ADD COLUMN part_slots_seen jsonb CHECK (jsonb_typeof(part_slots_seen) = 'object'),
    ADD COLUMN parts_sequence bigint NOT NULL DEFAULT 0;
