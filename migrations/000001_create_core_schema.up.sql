-- LinDesk 核心多租户 schema。
-- 这版 migration 只建立数据库结构和约束，不改变当前 Go 运行时仍使用的内存仓储。

CREATE TABLE tenants (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (status IN ('ACTIVE', 'DISABLED'))
);

CREATE TABLE users (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    email TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (status IN ('ACTIVE', 'DISABLED'))
);

CREATE TABLE roles (
    code TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    -- 先用 JSONB 保存权限数组，例如 ["order:read", "refund_request:create"]。
    -- 后续角色权限变复杂时，可以拆成 role_permissions 表。
    permissions JSONB NOT NULL DEFAULT '[]'::jsonb
);

CREATE TABLE tenant_members (
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    user_id TEXT NOT NULL REFERENCES users(id),
    role_code TEXT NOT NULL REFERENCES roles(code),
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 同一用户在同一租户下，同一个角色只能绑定一次。
    PRIMARY KEY (tenant_id, user_id, role_code),
    -- 给业务表做复合外键使用，确保操作人属于对应租户。
    UNIQUE (tenant_id, user_id)
);

CREATE TABLE orders (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    external_order_no TEXT NOT NULL,
    customer_id TEXT NOT NULL,
    payment_status TEXT NOT NULL,
    fulfillment_status TEXT NOT NULL,
    paid_amount BIGINT NOT NULL,
    refunded_amount BIGINT NOT NULL DEFAULT 0,
    currency TEXT NOT NULL,
    paid_at TIMESTAMPTZ NOT NULL,
    -- 原始订单快照，便于保留外部订单系统返回的完整信息。
    raw_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (payment_status IN ('PENDING', 'PAID')),
    CHECK (fulfillment_status IN ('NOT_SHIPPED', 'SHIPPED', 'DELIVERED', 'UNKNOWN')),
    CHECK (paid_amount >= 0),
    CHECK (refunded_amount >= 0),
    CHECK (refunded_amount <= paid_amount),
    -- 外部订单号只要求租户内唯一，不同租户可以有相同订单号。
    UNIQUE (tenant_id, external_order_no),
    -- 给 refund_requests 做复合外键使用，保证退款申请和订单属于同一租户。
    UNIQUE (tenant_id, id)
);

CREATE TABLE refund_requests (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    request_no TEXT NOT NULL,
    order_id TEXT NOT NULL,
    -- 创建退款时固化订单快照，后续审核和审计都以这份快照为准。
    order_snapshot JSONB NOT NULL,
    requested_amount BIGINT NOT NULL,
    reason_code TEXT NOT NULL,
    reason_note TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    submitted_by TEXT NOT NULL,
    submitted_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (requested_amount > 0),
    CHECK (status IN ('DRAFT', 'PENDING_REVIEW', 'APPROVED', 'PROCESSING', 'SUCCEEDED', 'REJECTED', 'FAILED', 'CANCELLED')),
    -- 退款申请必须引用同一租户下的订单，防止 tenant_id 与 order_id 不匹配。
    FOREIGN KEY (tenant_id, order_id) REFERENCES orders(tenant_id, id),
    -- 提交人必须是该租户成员，防止跨租户伪造操作人。
    FOREIGN KEY (tenant_id, submitted_by) REFERENCES tenant_members(tenant_id, user_id),
    -- 退款申请编号只要求租户内唯一。
    UNIQUE (tenant_id, request_no),
    -- 给 approvals/refund_transactions/audit_logs 做复合外键使用。
    UNIQUE (tenant_id, id)
);

CREATE TABLE approvals (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    refund_request_id TEXT NOT NULL,
    level INTEGER NOT NULL,
    status TEXT NOT NULL,
    assignee_id TEXT NOT NULL,
    decision_by TEXT,
    decision_at TIMESTAMPTZ,
    comment TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (level > 0),
    CHECK (status IN ('PENDING', 'APPROVED', 'REJECTED')),
    -- 审批记录必须属于同一租户下的退款申请。
    FOREIGN KEY (tenant_id, refund_request_id) REFERENCES refund_requests(tenant_id, id),
    -- 指定审批人必须是该租户成员。
    FOREIGN KEY (tenant_id, assignee_id) REFERENCES tenant_members(tenant_id, user_id),
    -- 实际审批人可为空；非空时必须是该租户成员。
    FOREIGN KEY (tenant_id, decision_by) REFERENCES tenant_members(tenant_id, user_id),
    -- 同一租户、同一退款申请、同一审批级别只能有一条审批记录。
    UNIQUE (tenant_id, refund_request_id, level)
);

CREATE TABLE refund_transactions (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    refund_request_id TEXT NOT NULL,
    provider TEXT NOT NULL,
    provider_refund_no TEXT,
    amount BIGINT NOT NULL,
    status TEXT NOT NULL,
    failure_reason TEXT NOT NULL DEFAULT '',
    processed_by TEXT NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (amount > 0),
    CHECK (status IN ('SUCCEEDED', 'FAILED')),
    CHECK (status <> 'SUCCEEDED' OR provider_refund_no IS NOT NULL),
    CHECK (status <> 'FAILED' OR failure_reason <> ''),
    -- 退款交易必须属于同一租户下的退款申请。
    FOREIGN KEY (tenant_id, refund_request_id) REFERENCES refund_requests(tenant_id, id),
    -- 财务操作人必须是该租户成员。
    FOREIGN KEY (tenant_id, processed_by) REFERENCES tenant_members(tenant_id, user_id)
);

CREATE UNIQUE INDEX refund_transactions_provider_refund_no_unique_idx
ON refund_transactions (tenant_id, provider, provider_refund_no)
WHERE provider_refund_no IS NOT NULL;

CREATE TABLE audit_logs (
    id TEXT PRIMARY KEY,
    tenant_id TEXT NOT NULL REFERENCES tenants(id),
    entity_type TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    action TEXT NOT NULL,
    operator_id TEXT NOT NULL,
    before_data JSONB NOT NULL DEFAULT '{}'::jsonb,
    after_data JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 操作人必须是该租户成员。
    FOREIGN KEY (tenant_id, operator_id) REFERENCES tenant_members(tenant_id, user_id)
);

-- 下面的索引用于高频的租户过滤和详情聚合查询。
CREATE INDEX orders_tenant_id_idx ON orders (tenant_id);
CREATE INDEX refund_requests_tenant_id_idx ON refund_requests (tenant_id);
CREATE INDEX approvals_tenant_id_idx ON approvals (tenant_id);
CREATE INDEX refund_transactions_tenant_id_idx ON refund_transactions (tenant_id);
CREATE INDEX audit_logs_tenant_id_idx ON audit_logs (tenant_id);
CREATE INDEX audit_logs_entity_idx ON audit_logs (tenant_id, entity_type, entity_id);
