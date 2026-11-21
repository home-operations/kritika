-- Checks the usage rows against the check that 0065_usage_rerank added. Its
-- own transaction, as validating takes a lock that lets the table be read
-- and written while it reads every row. That lock waits for a vacuum or
-- another validation of the table, so the wait gives up after a few seconds
-- rather than hold the leader's migrations, which try it again a retry
-- interval later (KRITIKA_LEADER_RETRY_INTERVAL).
SET LOCAL lock_timeout = '5s';
ALTER TABLE usage VALIDATE CONSTRAINT usage_role_check;
