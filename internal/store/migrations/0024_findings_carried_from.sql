-- The head a review last reported the finding at, whose line numbers the
-- row keeps, when an incremental re-review carried it on unchecked: neither
-- reported again nor listed as resolved. NULL on a finding its own review
-- reported.
ALTER TABLE findings ADD COLUMN carried_from text;
