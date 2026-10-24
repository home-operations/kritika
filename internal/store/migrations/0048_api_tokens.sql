-- api_tokens are the personal access tokens users create for agents that
-- read through the MCP endpoint. Each is a read-only copy of what its
-- creator's sign-in let them read: every account, or the forge accounts
-- in accounts, as sessions name them. It stands while the sign-in
-- configuration still matches grant_key, as a session does, and until
-- expires_at, NULL for never. Only the token's SHA-256 is kept.
CREATE TABLE api_tokens (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash      bytea       NOT NULL UNIQUE,
    user_id         uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name            text        NOT NULL,
    provider        text        NOT NULL,
    provider_origin text        NOT NULL DEFAULT '',
    all_accounts    boolean     NOT NULL,
    accounts        text[]      NOT NULL,
    grant_key       text        NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz,
    last_used_at    timestamptz
);
CREATE INDEX api_tokens_user_id_idx ON api_tokens (user_id);
