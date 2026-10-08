-- The part of a split review an agent step belongs to, from 1; 0 for a
-- step of a review that was not split. A part is a conversation of its
-- own: its steps are numbered, and their transcript deltas taken, apart
-- from the other parts' of the same run.
ALTER TABLE model_calls ADD COLUMN part int NOT NULL DEFAULT 0;
