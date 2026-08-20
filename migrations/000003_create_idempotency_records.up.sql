-- 幂等记录按租户、操作人、业务操作和客户端键唯一，避免不同身份互相复用结果。
CREATE TABLE idempotency_records (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    operation TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    status TEXT NOT NULL,
    response_status INTEGER NOT NULL,
    response_data JSONB NOT NULL,
    resource_type TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    completed_at TIMESTAMPTZ NOT NULL,
    CHECK (char_length(idempotency_key) BETWEEN 1 AND 255),
    CHECK (status IN ('COMPLETED')),
    CHECK (response_status BETWEEN 100 AND 599),
    FOREIGN KEY (tenant_id, actor_id) REFERENCES tenant_members(tenant_id, user_id) ON DELETE CASCADE,
    UNIQUE (tenant_id, actor_id, operation, idempotency_key)
);

-- created_at 索引用于后续按保留周期清理过期幂等记录。
CREATE INDEX idempotency_records_created_at_idx ON idempotency_records (created_at);
CREATE INDEX idempotency_records_resource_idx ON idempotency_records (tenant_id, resource_type, resource_id);
