-- The answers in its findings' threads that a re-review was shown, by each
-- finding's id in its prompt. Publication takes a finding as answered only
-- while its thread still holds these replies, unchanged.
ALTER TABLE reviews ADD COLUMN answers jsonb;
