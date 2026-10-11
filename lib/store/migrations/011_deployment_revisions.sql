CREATE TABLE IF NOT EXISTS deployment_revisions (
    id TEXT PRIMARY KEY,
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    generation BIGINT NOT NULL,
    model_version_id TEXT NOT NULL,
    model_version TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (deployment_id, generation)
);
CREATE INDEX IF NOT EXISTS idx_deployment_revisions_deployment ON deployment_revisions(deployment_id, generation DESC);
