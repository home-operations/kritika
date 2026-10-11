CREATE INDEX reviews_publication_cancel_job_idx ON reviews (river_job_id)
    WHERE status = 'prepared' AND cancel_requested_at IS NOT NULL;
