-- Validates the skip reason constraints 0045 added, under SHARE UPDATE
-- EXCLUSIVE locks, which reads and writes do not wait on.
ALTER TABLE context_packs VALIDATE CONSTRAINT context_packs_skip_reason_check;
ALTER TABLE reviews VALIDATE CONSTRAINT reviews_skip_reason_check;
