CREATE TABLE IF NOT EXISTS deployment_quota_releases (
    deployment_id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    gpu_type TEXT NOT NULL,
    gpu_count INTEGER NOT NULL CHECK (gpu_count >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
