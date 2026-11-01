-- A runner no longer connects to the database: the gateway writes what it
-- reports, as the application role under the run's account. So nothing uses
-- the runner_job policies, and the role that they were bound to loses
-- everything it was granted. The role itself is the cluster's to remove: an
-- owner cannot be assumed able to drop it. Only what the owner owns is revoked
-- from, since an extension's tables in the schema are not its to change, and
-- it never granted them.
DO $$
DECLARE
    runner_role name;
    target record;
BEGIN
    FOR runner_role IN
        SELECT DISTINCT r FROM pg_policies, unnest(roles) AS r
        WHERE schemaname = current_schema() AND policyname = 'runner_job' AND r <> 'public'
    LOOP
        FOR target IN
            SELECT tablename FROM pg_tables WHERE schemaname = current_schema() AND tableowner = current_user
        LOOP
            EXECUTE format('REVOKE ALL ON %I FROM %I', target.tablename, runner_role);
        END LOOP;
        FOR target IN
            SELECT sequencename FROM pg_sequences WHERE schemaname = current_schema() AND sequenceowner = current_user
        LOOP
            EXECUTE format('REVOKE ALL ON SEQUENCE %I FROM %I', target.sequencename, runner_role);
        END LOOP;
        EXECUTE format('REVOKE ALL ON SCHEMA %I FROM %I', current_schema(), runner_role);
    END LOOP;
    FOR target IN
        SELECT tablename FROM pg_policies WHERE schemaname = current_schema() AND policyname = 'runner_job'
    LOOP
        EXECUTE format('DROP POLICY runner_job ON %I', target.tablename);
    END LOOP;
END
$$;
