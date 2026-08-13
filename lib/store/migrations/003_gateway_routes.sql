-- 网关路由：控制面注册的可恢复模型服务端点。
CREATE TABLE IF NOT EXISTS gateway_routes (
    model         TEXT PRIMARY KEY,
    endpoint      TEXT NOT NULL,
    tenant_id     TEXT NOT NULL DEFAULT '',
    deployment_id TEXT NOT NULL DEFAULT '',
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
