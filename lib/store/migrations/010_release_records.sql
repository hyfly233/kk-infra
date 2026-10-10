CREATE TABLE IF NOT EXISTS release_records (
    id TEXT PRIMARY KEY,
    model_version_id TEXT NOT NULL,
    operator TEXT NOT NULL,
    status TEXT NOT NULL,
    stage_results JSONB NOT NULL DEFAULT '[]',
    benchmark JSONB,
    approved_by TEXT NOT NULL DEFAULT '',
    approval_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    released_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_release_records_version ON release_records(model_version_id, created_at DESC);
