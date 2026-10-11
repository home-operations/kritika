ALTER TABLE reviews ADD COLUMN publication jsonb;
CREATE INDEX reviews_publication_job_idx ON reviews (river_job_id) WHERE publication IS NOT NULL;
