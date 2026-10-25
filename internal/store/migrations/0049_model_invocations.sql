CREATE TABLE model_invocations (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES accounts(id),
    runner_run_id uuid REFERENCES runner_runs(id),
    state text NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved', 'completed', 'settled', 'uncertain')),
    token_hash bytea,
    reserved_tokens bigint NOT NULL DEFAULT 0 CHECK (reserved_tokens >= 0),
    usage jsonb NOT NULL,
    answered boolean,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    settled_at timestamptz,
    CHECK ((state IN ('reserved', 'uncertain')) = (answered IS NULL))
);

CREATE INDEX model_invocations_pending_idx ON model_invocations(created_at)
    WHERE state IN ('reserved', 'completed');
CREATE INDEX model_invocations_account_idx ON model_invocations(account_id, created_at);

ALTER TABLE model_invocations ENABLE ROW LEVEL SECURITY;
CREATE POLICY account_isolation ON model_invocations
    USING (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid);
