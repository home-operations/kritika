-- abandon_after is the abandonment window of the replica that wrote a job's
-- last beat: how long after it the leader counts the job abandoned. A
-- replica fences its job a beat before that, at its own cadence. The leader
-- rescues by the job's window rather than its own, so that replicas that
-- run different heartbeat settings, as during a rollout that changes them,
-- never have a live job rescued. NULL is a beat of a replica from before the
-- column, which the leader judges by its own window.
ALTER TABLE job_heartbeats ADD COLUMN abandon_after interval;
