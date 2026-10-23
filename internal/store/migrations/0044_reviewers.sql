-- A review may run several reviewers beside the default one, each an
-- agent of its own. A finding names the reviewers that raised it, none
-- being the default reviewer's; a run's token grants each reviewer's
-- models, by name; and a model call names the reviewer it was for, ''
-- being the default reviewer's. Each column's default is a constant, so
-- adding it rewrites no row.
ALTER TABLE findings ADD COLUMN reviewers text[] NOT NULL DEFAULT '{}';
ALTER TABLE gateway_tokens ADD COLUMN reviewers jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(reviewers) = 'object');
ALTER TABLE model_calls ADD COLUMN reviewer text NOT NULL DEFAULT '';
