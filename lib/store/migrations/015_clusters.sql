CREATE TABLE IF NOT EXISTS clusters (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    endpoint TEXT NOT NULL DEFAULT '',
    adapter_url TEXT NOT NULL DEFAULT '',
    kubeconfig_ciphertext BYTEA NOT NULL,
    labels JSONB NOT NULL DEFAULT '{}'::jsonb,
    supported_runtimes JSONB NOT NULL DEFAULT '[]'::jsonb,
    allowed_tenants JSONB NOT NULL DEFAULT '[]'::jsonb,
    gpu_capacity JSONB NOT NULL DEFAULT '[]'::jsonb,
    health_status TEXT NOT NULL DEFAULT 'unknown',
    last_heartbeat TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE clusters ADD COLUMN IF NOT EXISTS adapter_url TEXT NOT NULL DEFAULT '';
ALTER TABLE clusters ADD COLUMN IF NOT EXISTS allowed_tenants JSONB NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE deployments ADD COLUMN IF NOT EXISTS cluster_id TEXT NOT NULL DEFAULT '';
ALTER TABLE gateway_routes ADD COLUMN IF NOT EXISTS cluster_id TEXT NOT NULL DEFAULT '';
