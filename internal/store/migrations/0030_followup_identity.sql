ALTER TABLE followups DROP CONSTRAINT followups_pull_request_id_comment_id_key;
ALTER TABLE followups ADD CONSTRAINT followups_pull_request_id_comment_id_inline_key
    UNIQUE (pull_request_id, comment_id, inline);
