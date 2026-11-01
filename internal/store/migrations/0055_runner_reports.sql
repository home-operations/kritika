-- The upload of a run's index that may stage. The gateway stages each upload's
-- batches in transactions of their own, so a stale upload must be told apart.
ALTER TABLE runner_runs ADD COLUMN index_upload uuid;

-- The service now reads what a runner saved, and jsonb does not read back what
-- it was given: a number such as 1e65535 is short to send and tens of kilobytes
-- to read. As text, a checkpoint reads back at the size that the gateway bounded.
-- The gateway only takes valid JSON here, and the runner decodes it.
ALTER TABLE review_part_checkpoints ALTER COLUMN payload TYPE text USING payload::text;
