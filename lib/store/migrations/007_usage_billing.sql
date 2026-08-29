CREATE TABLE IF NOT EXISTS rate_cards (
    tenant_id TEXT NOT NULL,
    gpu_type TEXT NOT NULL,
    input_token_per_million NUMERIC(18,6) NOT NULL DEFAULT 0,
    output_token_per_million NUMERIC(18,6) NOT NULL DEFAULT 0,
    gpu_hour NUMERIC(18,6) NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, gpu_type)
);
CREATE TABLE IF NOT EXISTS usage_records (
    id BIGSERIAL PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    deployment_id TEXT NOT NULL,
    model_id TEXT NOT NULL DEFAULT '',
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    latency_ms BIGINT NOT NULL DEFAULT 0,
    failed BOOLEAN NOT NULL DEFAULT false,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_usage_records_tenant_created ON usage_records(tenant_id, created_at);
CREATE TABLE IF NOT EXISTS daily_usage (
    usage_date DATE NOT NULL,
    tenant_id TEXT NOT NULL,
    deployment_id TEXT NOT NULL,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    request_count BIGINT NOT NULL DEFAULT 0,
    failed_count BIGINT NOT NULL DEFAULT 0,
    gpu_replica_seconds BIGINT NOT NULL DEFAULT 0,
    estimated_cost NUMERIC(18,6) NOT NULL DEFAULT 0,
    PRIMARY KEY (usage_date, tenant_id, deployment_id)
);
