-- What the forge last said of who can read the repository: public, private
-- or internal, as GitHub names them, and '' until it says. The next listing
-- of the App's repositories fills it in.
ALTER TABLE repositories ADD COLUMN visibility text NOT NULL DEFAULT ''
    CHECK (visibility IN ('', 'public', 'private', 'internal'));
