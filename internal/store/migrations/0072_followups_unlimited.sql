-- A follow-up is answered, ignored or failed. A mention that an hourly limit
-- held back is recorded as ignored, with that reason. Its reply was the
-- limit's notice, which went on the conversation even for a question asked
-- in a thread, so the reply is not kept: the dashboard links an inline
-- follow-up's reply in the thread.
--
-- Dropping the check takes the exclusive lock of followups first, so no
-- follow-up is recorded between the statements below. A wait for that lock
-- would hold up every follow-up behind it. So the wait gives up after a few
-- seconds, the migration fails, and the leader tries it again a retry
-- interval later (KRITIKA_LEADER_RETRY_INTERVAL).
SET LOCAL lock_timeout = '5s';
ALTER TABLE followups DROP CONSTRAINT followups_status_check;
UPDATE followups SET status = 'ignored', reason = 'the pull request had its follow-ups for the hour', reply_comment_id = NULL
    WHERE status = 'limited';
ALTER TABLE followups ADD CONSTRAINT followups_status_check CHECK (status IN ('answered', 'ignored', 'failed'));
