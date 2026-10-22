-- A GitLab instance is a forge a connection and its accounts may be on.
ALTER TABLE accounts DROP CONSTRAINT accounts_forge_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_forge_check CHECK (forge IN ('github', 'forgejo', 'gitlab'));
ALTER TABLE connections DROP CONSTRAINT connections_forge_check;
ALTER TABLE connections ADD CONSTRAINT connections_forge_check CHECK (forge IN ('github', 'forgejo', 'gitlab'));
