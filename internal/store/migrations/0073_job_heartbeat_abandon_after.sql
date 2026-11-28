-- abandon_after is the abandonment window of the replica that wrote a job's
-- last beat, and abandon_after_at that beat's heartbeat_at. The leader
-- rescues a job by that window rather than its own, so a rollout that
-- changes heartbeat settings never has a live job rescued. A leader from
-- before the columns judges every job by 180 seconds. A beat whose
-- heartbeat_at is not abandon_after_at is from a replica from before the
-- columns, which writes heartbeat_at alone, and its window is 180 seconds.
ALTER TABLE job_heartbeats ADD COLUMN abandon_after interval NOT NULL DEFAULT interval '180 seconds',
    ADD COLUMN abandon_after_at timestamptz;
