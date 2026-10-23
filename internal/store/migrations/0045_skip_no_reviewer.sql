-- A review none of whose reviewers applies to the change is skipped as
-- no_reviewer. The constraints admit every value the old ones did, so
-- they are added NOT VALID, which checks no existing row and holds each
-- table's lock only briefly; 0046 validates them under a lock that lets
-- reads and writes go on.
ALTER TABLE context_packs DROP CONSTRAINT context_packs_skip_reason_check;
ALTER TABLE context_packs ADD CONSTRAINT context_packs_skip_reason_check
    CHECK (skip_reason IN ('', 'only_skipped_paths', 'unchanged_patch', 'too_large', 'filtered', 'no_reviewer')) NOT VALID;
ALTER TABLE reviews DROP CONSTRAINT reviews_skip_reason_check;
ALTER TABLE reviews ADD CONSTRAINT reviews_skip_reason_check
    CHECK (skip_reason IN ('', 'disabled', 'filtered', 'only_skipped_paths', 'unchanged_patch', 'too_large', 'no_reviewer')) NOT VALID;
