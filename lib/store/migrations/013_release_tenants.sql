ALTER TABLE release_records
    ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_release_records_tenant
    ON release_records(tenant_id, created_at DESC);
