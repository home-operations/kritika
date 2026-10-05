-- The runner judges the include and exclude conditions only a diff can
-- answer, so it may skip a review as filtered, and records the name of the
-- condition that decided, which the commit status states.
ALTER TABLE context_packs DROP CONSTRAINT context_packs_skip_reason_check;
ALTER TABLE context_packs ADD CONSTRAINT context_packs_skip_reason_check
    CHECK (skip_reason IN ('', 'only_skipped_paths', 'unchanged_patch', 'too_large', 'filtered'));
ALTER TABLE context_packs ADD COLUMN skip_detail text NOT NULL DEFAULT '';
