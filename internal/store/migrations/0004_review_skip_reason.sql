-- A skipped review records why, whoever decided: until now only the
-- repository's own reasons fit, and a skip the runner decided (a diff over
-- maxChangedLines, a patch unchanged since the last review) was on its
-- context pack alone, or, for a bot's unchanged patch caught before a
-- runner started, nowhere.
ALTER TABLE reviews DROP CONSTRAINT reviews_skip_reason_check;
ALTER TABLE reviews ADD CONSTRAINT reviews_skip_reason_check
    CHECK (skip_reason IN ('', 'disabled', 'filtered', 'only_skipped_paths', 'unchanged_patch', 'too_large'));

-- The reviews skipped before this: the reason on the context pack, or,
-- with no pack and no error, the unchanged bot patch, the one skip that
-- left neither.
UPDATE reviews v SET skip_reason = coalesce(
        (SELECT nullif(cp.skip_reason, '') FROM context_packs cp JOIN runner_runs rr ON rr.id = cp.runner_run_id
            WHERE rr.review_id = v.id ORDER BY rr.created_at DESC LIMIT 1),
        CASE WHEN v.error = '' THEN 'unchanged_patch' ELSE '' END)
    WHERE v.status = 'skipped' AND v.skip_reason = '';
