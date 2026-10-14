-- Older workers still upsert on (pull_request_id, comment_id). Keep that
-- constraint until all writers use the scoped key and transcript/session
-- identity and queued jobs can distinguish both comment namespaces.
ALTER TABLE followups ADD CONSTRAINT followups_pull_request_id_comment_id_inline_key
    UNIQUE (pull_request_id, comment_id, inline);
