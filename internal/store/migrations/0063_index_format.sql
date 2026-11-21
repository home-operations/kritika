-- The index format: how a build chunks a tree and what text it embeds for
-- each chunk. A generation records the format that built it, and
-- index_schema the format that the leader's build applied. A generation of
-- another format is rebuilt by the onboarding feeder, a few at a time. Every
-- index built before was format 1, and so is every run that a build without
-- the column inserts while a rollout is part done.
--
-- Each column takes its table's exclusive lock, which a wait for it would
-- hold up every index job's reads behind. So a wait gives up after a few
-- seconds, the migration fails, and the leader tries it again a retry
-- interval later (KRITIKA_LEADER_RETRY_INTERVAL).
SET LOCAL lock_timeout = '5s';
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS index_format int NOT NULL DEFAULT 1;
ALTER TABLE index_schema ADD COLUMN IF NOT EXISTS index_format int NOT NULL DEFAULT 1;

-- A full build truncated by the indexer's budget leaves out the files past
-- it. The runner says so in its pack, and the run records it. An
-- incremental step leaves the generation's flag as it is.
ALTER TABLE index_packs ADD COLUMN IF NOT EXISTS truncated boolean NOT NULL DEFAULT false;
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS truncated boolean NOT NULL DEFAULT false;
