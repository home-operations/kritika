-- A review canceled because a newer review of its pull request started
-- points at the review that owns the pull request's result now.
ALTER TABLE reviews ADD COLUMN replaced_by_review_id uuid REFERENCES reviews (id);
