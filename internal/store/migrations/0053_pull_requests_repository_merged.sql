-- The judge is told how the last merged change to each dependency a change
-- moves was rated, read from the newest completed review of a repository's
-- merged pull requests, newest merge first.
CREATE INDEX pull_requests_repository_merged_idx ON pull_requests (repository_id, closed_at DESC) WHERE merged;
