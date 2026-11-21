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
--
-- The migration waits for index_runs' lock alone, while it holds none that
-- an index job waits for, and takes the other tables' at once or fails. A
-- job of a build from before index formats reads its pack and then writes
-- its run (ReadIndexPack). Had the migration held index_runs while it waited
-- for index_packs, that write would wait for the migration while the
-- migration waits for the job: a deadlock that can abort the job.
SET LOCAL lock_timeout = '5s';
LOCK TABLE index_runs IN ACCESS EXCLUSIVE MODE;
LOCK TABLE index_schema, index_packs IN ACCESS EXCLUSIVE MODE NOWAIT;
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS index_format int NOT NULL DEFAULT 1;
ALTER TABLE index_schema ADD COLUMN IF NOT EXISTS index_format int NOT NULL DEFAULT 1;

-- A full build truncated by the indexer's budget leaves out the files past
-- it. The runner says so in its pack, and the run records it. An
-- incremental step leaves the generation's flag as it is.
ALTER TABLE index_packs ADD COLUMN IF NOT EXISTS truncated boolean NOT NULL DEFAULT false;
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS truncated boolean NOT NULL DEFAULT false;

-- An incremental step of one format can advance a generation of another, as
-- a build from before index formats does while a rollout is part done. The
-- generation then holds chunks of both formats, and is rebuilt. A completed
-- step records the generation that it advanced, stepped_run_id: the
-- repository's active generation when the step completes. A step publishes
-- holding the repository's lock, and is superseded unless the generation
-- that it read is still the active one, so that is the generation it
-- advanced. A trigger records it, so that the steps of a build without the
-- column record it too, and with no order of clock times, of when the step
-- started and when the generation was published, which a clock that steps
-- back reverses.
ALTER TABLE index_runs ADD COLUMN IF NOT EXISTS stepped_run_id uuid;
CREATE OR REPLACE FUNCTION kritika_index_stepped() RETURNS trigger AS $$
BEGIN
    IF NEW.status = 'completed' AND NEW.mode = 'incremental' AND (TG_OP = 'INSERT' OR OLD.status IS DISTINCT FROM 'completed') THEN
        NEW.stepped_run_id := (SELECT active_index_run_id FROM repositories WHERE id = NEW.repository_id);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
CREATE OR REPLACE TRIGGER kritika_index_stepped
    BEFORE INSERT OR UPDATE OF status ON index_runs
    FOR EACH ROW
    EXECUTE FUNCTION kritika_index_stepped();
