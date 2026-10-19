ALTER TABLE runner_runs
    ADD COLUMN part_slots integer NOT NULL DEFAULT 1 CHECK (part_slots > 0),
    ADD COLUMN parts_remaining integer CHECK (parts_remaining >= 0);
