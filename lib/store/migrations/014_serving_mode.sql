ALTER TABLE deployments
    ADD COLUMN IF NOT EXISTS serving_mode TEXT NOT NULL DEFAULT 'unified';
