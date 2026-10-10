-- Provider credentials belong to the service; runner roles receive no grant.
CREATE TABLE chatgpt_sessions (
    key               text        PRIMARY KEY,
    credentials       jsonb       NOT NULL,
    signed_out_at     timestamptz,
    signed_out_reason text        NOT NULL DEFAULT '',
    updated_at        timestamptz NOT NULL DEFAULT now()
);
