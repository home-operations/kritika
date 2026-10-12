-- Run IDs are public metadata, including in LISTEN/NOTIFY payloads. A
-- separate capability is required even when a runner knows another ID.
ALTER TABLE runner_runs ADD COLUMN access_token_hash bytea;

ALTER POLICY runner_job ON runner_runs
    USING (id = NULLIF(current_setting('app.runner_job_id', true), '')::uuid
           AND access_token_hash = sha256(convert_to(current_setting('app.runner_token', true), 'UTF8')))
    WITH CHECK (id = NULLIF(current_setting('app.runner_job_id', true), '')::uuid
                AND access_token_hash = sha256(convert_to(current_setting('app.runner_token', true), 'UTF8')));

DO $$
DECLARE t text;
BEGIN
    FOR t IN SELECT tablename FROM pg_policies
             WHERE schemaname = current_schema() AND policyname = 'runner_job' AND tablename <> 'runner_runs'
    LOOP
        EXECUTE format('ALTER POLICY runner_job ON %I
            USING (runner_run_id = NULLIF(current_setting(''app.runner_job_id'', true), '''')::uuid
                   AND EXISTS (SELECT 1 FROM runner_runs r WHERE r.id = runner_run_id))', t);
    END LOOP;
END
$$;
