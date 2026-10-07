-- A review agent's conversation with its model, written by its runner: the
-- system prompt, the tools and every message as the last step sent them,
-- with the model's answer, so the pull request's next re-review can carry
-- it on while the provider still caches it. A cached prefix must match byte
-- for byte, so it is kept unmasked: the runner keeps none that holds its
-- run's secrets, no dashboard view reads it, and the leader deletes it
-- within hours. session is the conversation the provider was told the
-- steps belong to, and tokens its size as the last step counted it.
CREATE TABLE agent_conversations (
    runner_run_id uuid        PRIMARY KEY REFERENCES runner_runs (id),
    account_id    uuid        NOT NULL REFERENCES accounts (id),
    session       text        NOT NULL,
    conversation  text        NOT NULL,
    tokens        bigint      NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX agent_conversations_created_at_idx ON agent_conversations (created_at);

ALTER TABLE agent_conversations ENABLE ROW LEVEL SECURITY;
CREATE POLICY account_isolation ON agent_conversations
    USING      (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid)
    WITH CHECK (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid);
CREATE POLICY runner_job ON agent_conversations
    USING      (runner_run_id = NULLIF(current_setting('app.runner_job_id', true), '')::uuid)
    WITH CHECK (runner_run_id = NULLIF(current_setting('app.runner_job_id', true), '')::uuid
                AND account_id = (SELECT r.account_id FROM runner_runs r WHERE r.id = runner_run_id));
