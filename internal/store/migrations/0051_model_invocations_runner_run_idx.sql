-- A failed run is asked whether any of its steps was reserved; the
-- account index would scan every reservation the account ever made.
CREATE INDEX model_invocations_runner_run_idx ON model_invocations (runner_run_id) WHERE runner_run_id IS NOT NULL;
