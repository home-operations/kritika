-- The base-side names of a change's renames, which changed_paths names by
-- their head-side names. The diff that would show them is cut to a size.
ALTER TABLE context_packs ADD COLUMN renamed_from text[] NOT NULL DEFAULT '{}';
