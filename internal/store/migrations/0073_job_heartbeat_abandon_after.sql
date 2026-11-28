-- abandon_after is the abandonment window of the replica that wrote a job's
-- last beat. The leader rescues a job by that window rather than its own, so
-- a rollout that changes heartbeat settings never has a live job rescued.
-- The default is the window of a replica from before the column.
ALTER TABLE job_heartbeats ADD COLUMN abandon_after interval NOT NULL DEFAULT interval '180 seconds';
