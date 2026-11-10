-- A split review's parts run in the slots of their reviewer's model: the
-- worker grants slots per model and the runner reports its unfinished parts
-- per model. Both are objects keyed by the model as configured. A run in
-- flight keeps its one count under "default", which no new run reads.
ALTER TABLE runner_runs
    DROP CONSTRAINT runner_runs_part_slots_check,
    DROP CONSTRAINT runner_runs_parts_remaining_check,
    ALTER COLUMN part_slots DROP DEFAULT,
    ALTER COLUMN part_slots TYPE jsonb USING jsonb_build_object('default', part_slots),
    ALTER COLUMN part_slots SET DEFAULT '{}'::jsonb,
    ADD CONSTRAINT runner_runs_part_slots_check CHECK (jsonb_typeof(part_slots) = 'object'),
    ALTER COLUMN parts_remaining TYPE jsonb USING CASE WHEN parts_remaining IS NULL THEN NULL
        ELSE jsonb_build_object('default', parts_remaining) END,
    ADD CONSTRAINT runner_runs_parts_remaining_check CHECK (jsonb_typeof(parts_remaining) = 'object');
