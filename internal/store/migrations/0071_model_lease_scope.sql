-- The slots of a model belong to a scope: the empty one for a provider of
-- the instance, which every account shares, or the id of the account that
-- declares the provider. account_id is now the account of the job that
-- holds a slot, null while the slot is free, which is how the slots that
-- each account holds of a shared model are counted.
--
-- The rows are leases of at most a few minutes and are made again on
-- demand, so they are deleted. A lease in flight is forgotten: its holder
-- renews and lets go of a row that is gone, and until the reviews under way
-- end, more calls than the limit can run. A replica of the version before
-- this one, still running while a deployment rolls, inserts no scope: its
-- claim of a slot fails, which costs its job an attempt.
--
-- The lock keeps a slot from being inserted between the statements below.
-- A wait for it would hold up every claim of a slot behind it. So the wait
-- gives up after a few seconds, the migration fails, and the leader tries it
-- again a retry interval later (KRITIKA_LEADER_RETRY_INTERVAL).
SET LOCAL lock_timeout = '5s';
LOCK TABLE model_leases IN ACCESS EXCLUSIVE MODE;
DELETE FROM model_leases;
ALTER TABLE model_leases DROP CONSTRAINT model_leases_pkey;
ALTER TABLE model_leases
    ADD COLUMN scope text NOT NULL,
    ALTER COLUMN account_id DROP NOT NULL,
    ADD PRIMARY KEY (scope, model_key, slot);

-- An account reads the shared slots and its own, and writes no other
-- account as a slot's holder.
DROP POLICY account_isolation ON model_leases;
CREATE POLICY account_isolation ON model_leases
    USING      (NULLIF(current_setting('app.account_id', true), '') IS NOT NULL
        AND scope IN ('', NULLIF(current_setting('app.account_id', true), '')::uuid::text))
    WITH CHECK (NULLIF(current_setting('app.account_id', true), '') IS NOT NULL
        AND scope IN ('', NULLIF(current_setting('app.account_id', true), '')::uuid::text)
        AND (account_id IS NULL OR account_id = NULLIF(current_setting('app.account_id', true), '')::uuid));
-- A shared slot that a live lease of another account holds is not an
-- account's to take, let go or delete. The policies are restrictive, so
-- they only narrow what account_isolation allows, for whichever role.
CREATE POLICY held_by_another_update ON model_leases AS RESTRICTIVE FOR UPDATE
    USING (job_id IS NULL OR expires_at < now()
        OR account_id = NULLIF(current_setting('app.account_id', true), '')::uuid);
CREATE POLICY held_by_another_delete ON model_leases AS RESTRICTIVE FOR DELETE
    USING (job_id IS NULL OR expires_at < now()
        OR account_id = NULLIF(current_setting('app.account_id', true), '')::uuid);
