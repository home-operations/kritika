-- When a user last signed in, kept on the user since their sessions are
-- swept once they expire; NULL until their next sign-in. Adding a column
-- with no default rewrites no row.
ALTER TABLE users ADD COLUMN last_sign_in_at timestamptz;
