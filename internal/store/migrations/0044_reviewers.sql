-- A review may run several reviewers beside the default one, each an
-- agent of its own. A finding names the reviewers that raised it, none
-- being the default reviewer's; a run's token grants each reviewer's
-- models, by name; a model call names the reviewer it was for, '' being
-- the default reviewer's; and a review none of whose reviewers applies to
-- the change is skipped as no_reviewer.
ALTER TABLE findings ADD COLUMN reviewers text[] NOT NULL DEFAULT '{}';
ALTER TABLE gateway_tokens ADD COLUMN reviewers jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(reviewers) = 'object');
ALTER TABLE model_calls ADD COLUMN reviewer text NOT NULL DEFAULT '';

ALTER TABLE context_packs DROP CONSTRAINT context_packs_skip_reason_check;
ALTER TABLE context_packs ADD CONSTRAINT context_packs_skip_reason_check
    CHECK (skip_reason IN ('', 'only_skipped_paths', 'unchanged_patch', 'too_large', 'filtered', 'no_reviewer'));
ALTER TABLE reviews DROP CONSTRAINT reviews_skip_reason_check;
ALTER TABLE reviews ADD CONSTRAINT reviews_skip_reason_check
    CHECK (skip_reason IN ('', 'disabled', 'filtered', 'only_skipped_paths', 'unchanged_patch', 'too_large', 'no_reviewer'));
