-- Do not guess historical ownership. Empty ownership is quarantined by API auth.
ALTER TABLE models ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS models_tenant_id_idx ON models (tenant_id);
