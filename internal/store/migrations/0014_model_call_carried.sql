-- An agent step recorded as a delta against the transcript of the run its
-- run carried on names that run, so the transcript view can say where the
-- messages before it are, while the review still runs too.
ALTER TABLE model_calls ADD COLUMN carried_from uuid REFERENCES runner_runs (id);
