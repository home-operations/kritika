-- The hosts the gateway refused a run, for the dashboard: an admin sees
-- what to add to egress.allow from real refusals. host is the name the
-- agent asked for, lowercased and without its port, never an address it
-- resolved to, so a private address stays out of the dashboard; it is
-- steered by the content under review, so the dashboard shows it as text
-- alone. One row per run, kind, host and reason, counted; the gateway
-- bounds the rows a run has, so a looping agent cannot fill the table,
-- and the leader sweeps them with the run's model calls.
CREATE TABLE egress_refusals (
    runner_run_id uuid        NOT NULL REFERENCES runner_runs (id),
    account_id    uuid        NOT NULL REFERENCES accounts (id),
    repository_id uuid        REFERENCES repositories (id),
    kind          text        NOT NULL CHECK (kind IN ('connect', 'http')),
    host          text        NOT NULL,
    reason        text        NOT NULL,
    count         bigint      NOT NULL DEFAULT 1 CHECK (count > 0),
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (runner_run_id, kind, host, reason)
);
CREATE INDEX egress_refusals_account_seen_idx ON egress_refusals (account_id, last_seen_at DESC);
CREATE INDEX egress_refusals_last_seen_idx ON egress_refusals (last_seen_at);

ALTER TABLE egress_refusals ENABLE ROW LEVEL SECURITY;
CREATE POLICY account_isolation ON egress_refusals
    USING      (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid)
    WITH CHECK (account_id = NULLIF(current_setting('app.account_id', true), '')::uuid);
