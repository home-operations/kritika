-- A chatgpt provider's live token set: the record its sign-in issued, as
-- the configuration seeded it, then as the leader's refreshes replace it.
-- Every replica reads the set; only the leader writes it, so the rotating
-- refresh token is never used twice. Instance-level, like config_state:
-- an account's own provider is still the operator's secret.
CREATE TABLE chatgpt_sessions (
    client_id         text        PRIMARY KEY,
    -- The seeded record's tokens, hashed, so a new sign-in in the
    -- configuration replaces the live set and the same one leaves it be.
    seed_hash         text        NOT NULL,
    credentials       jsonb       NOT NULL,
    signed_out_at     timestamptz,
    signed_out_reason text        NOT NULL DEFAULT '',
    updated_at        timestamptz NOT NULL DEFAULT now()
);
