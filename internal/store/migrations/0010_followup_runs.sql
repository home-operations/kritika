-- A follow-up is answered by an agent in a runner of its own kind, whose
-- run has neither a review nor an index generation for a parent.
ALTER TABLE runner_runs DROP CONSTRAINT runner_runs_kind_check;
ALTER TABLE runner_runs ADD CONSTRAINT runner_runs_kind_check CHECK (kind IN ('review', 'index', 'followup'));

-- A follow-up's run token names the comment it answers, and a review only
-- when the pull request has a completed one.
ALTER TABLE gateway_tokens ALTER COLUMN review_id DROP NOT NULL;
ALTER TABLE gateway_tokens ADD COLUMN followup_comment_id bigint;
