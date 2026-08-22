-- LinDesk 本地演示数据。
-- 这批数据需要和 internal/auth 的 Demo* 数据保持一致，用于 PostgreSQL 模式下跑通退款闭环。

INSERT INTO tenants (id, name, status, created_at) VALUES
    ('tenant_demo', 'LinDesk Demo 电商', 'ACTIVE', '2026-08-06T00:00:00Z'),
    ('tenant_acme', 'Acme 零售旗舰店', 'ACTIVE', '2026-08-06T00:00:00Z')
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    status = EXCLUDED.status;

INSERT INTO users (id, name, email, password_hash, status, created_at) VALUES
    ('user_cs_001', '客服一号', 'cs@lindesk.local', 'pbkdf2-sha256:210000:bGluZGVzay1kZW1vLXYx:oeifo07otBNPOL-vyic07_7zqgf-B-4oVgd8GaMPzkk', 'ACTIVE', '2026-08-06T00:00:00Z'),
    ('user_supervisor_001', '主管一号', 'supervisor@lindesk.local', 'pbkdf2-sha256:210000:bGluZGVzay1kZW1vLXYx:oeifo07otBNPOL-vyic07_7zqgf-B-4oVgd8GaMPzkk', 'ACTIVE', '2026-08-06T00:00:00Z'),
    ('user_finance_001', '财务一号', 'finance@lindesk.local', 'pbkdf2-sha256:210000:bGluZGVzay1kZW1vLXYx:oeifo07otBNPOL-vyic07_7zqgf-B-4oVgd8GaMPzkk', 'ACTIVE', '2026-08-06T00:00:00Z'),
    ('user_finance_supervisor_001', '财务主管一号', 'finance.supervisor@lindesk.local', 'pbkdf2-sha256:210000:bGluZGVzay1kZW1vLXYx:oeifo07otBNPOL-vyic07_7zqgf-B-4oVgd8GaMPzkk', 'ACTIVE', '2026-08-06T00:00:00Z'),
    ('user_admin_001', '管理员一号', 'admin@lindesk.local', 'pbkdf2-sha256:210000:bGluZGVzay1kZW1vLXYx:oeifo07otBNPOL-vyic07_7zqgf-B-4oVgd8GaMPzkk', 'ACTIVE', '2026-08-06T00:00:00Z'),
    ('user_acme_cs_001', 'Acme 客服一号', 'acme.cs@lindesk.local', 'pbkdf2-sha256:210000:bGluZGVzay1kZW1vLXYx:oeifo07otBNPOL-vyic07_7zqgf-B-4oVgd8GaMPzkk', 'ACTIVE', '2026-08-06T00:00:00Z'),
    ('user_acme_supervisor_001', 'Acme 主管一号', 'acme.supervisor@lindesk.local', 'pbkdf2-sha256:210000:bGluZGVzay1kZW1vLXYx:oeifo07otBNPOL-vyic07_7zqgf-B-4oVgd8GaMPzkk', 'ACTIVE', '2026-08-06T00:00:00Z'),
    ('user_acme_finance_001', 'Acme 财务一号', 'acme.finance@lindesk.local', 'pbkdf2-sha256:210000:bGluZGVzay1kZW1vLXYx:oeifo07otBNPOL-vyic07_7zqgf-B-4oVgd8GaMPzkk', 'ACTIVE', '2026-08-06T00:00:00Z'),
    ('user_acme_finance_supervisor_001', 'Acme 财务主管一号', 'acme.finance.supervisor@lindesk.local', 'pbkdf2-sha256:210000:bGluZGVzay1kZW1vLXYx:oeifo07otBNPOL-vyic07_7zqgf-B-4oVgd8GaMPzkk', 'ACTIVE', '2026-08-06T00:00:00Z')
ON CONFLICT (id) DO UPDATE SET
    name = EXCLUDED.name,
    email = EXCLUDED.email,
    password_hash = EXCLUDED.password_hash,
    status = EXCLUDED.status;

INSERT INTO roles (code, name, permissions) VALUES
    ('CUSTOMER_SERVICE', '客服专员', '["order:read", "refund_request:create", "refund_request:read"]'::jsonb),
    ('SUPERVISOR', '客服主管', '["order:read", "refund_request:read", "refund_request:review"]'::jsonb),
    ('FINANCE', '财务人员', '["refund_request:read", "refund_transaction:write"]'::jsonb),
    ('FINANCE_SUPERVISOR', '财务主管', '["refund_request:read", "refund_request:high_amount_review"]'::jsonb),
    ('TENANT_ADMIN', '企业管理员', '["order:read", "refund_request:create", "refund_request:read", "refund_request:review", "refund_request:high_amount_review", "refund_transaction:write", "tenant_member:manage"]'::jsonb)
ON CONFLICT (code) DO UPDATE SET
    name = EXCLUDED.name,
    permissions = EXCLUDED.permissions;

INSERT INTO tenant_members (tenant_id, user_id, role_code, joined_at) VALUES
    ('tenant_demo', 'user_cs_001', 'CUSTOMER_SERVICE', '2026-08-06T00:00:00Z'),
    ('tenant_demo', 'user_supervisor_001', 'SUPERVISOR', '2026-08-06T00:00:00Z'),
    ('tenant_demo', 'user_finance_001', 'FINANCE', '2026-08-06T00:00:00Z'),
    ('tenant_demo', 'user_finance_supervisor_001', 'FINANCE_SUPERVISOR', '2026-08-06T00:00:00Z'),
    ('tenant_demo', 'user_admin_001', 'TENANT_ADMIN', '2026-08-06T00:00:00Z'),
    ('tenant_acme', 'user_acme_cs_001', 'CUSTOMER_SERVICE', '2026-08-06T00:00:00Z'),
    ('tenant_acme', 'user_acme_supervisor_001', 'SUPERVISOR', '2026-08-06T00:00:00Z'),
    ('tenant_acme', 'user_acme_finance_001', 'FINANCE', '2026-08-06T00:00:00Z'),
    ('tenant_acme', 'user_acme_finance_supervisor_001', 'FINANCE_SUPERVISOR', '2026-08-06T00:00:00Z')
ON CONFLICT (tenant_id, user_id, role_code) DO UPDATE SET
    joined_at = EXCLUDED.joined_at;

INSERT INTO orders (
    id, tenant_id, external_order_no, customer_id, payment_status,
    fulfillment_status, paid_amount, refunded_amount, currency, paid_at, raw_snapshot
) VALUES
    ('order_1001', 'tenant_demo', 'LD202608040001', 'customer_1001', 'PAID', 'NOT_SHIPPED', 12900, 0, 'CNY', '2026-08-04T02:30:00Z', '{"source":"demo_seed"}'::jsonb),
    ('order_1002', 'tenant_demo', 'LD202608040002', 'customer_1002', 'PAID', 'SHIPPED', 8800, 0, 'CNY', '2026-08-04T02:30:00Z', '{"source":"demo_seed"}'::jsonb),
    ('order_1003', 'tenant_demo', 'LD202608040003', 'customer_1003', 'PENDING', 'NOT_SHIPPED', 6600, 0, 'CNY', '2026-08-04T02:30:00Z', '{"source":"demo_seed"}'::jsonb),
    ('order_1004', 'tenant_demo', 'LD202608040004', 'customer_1004', 'PAID', 'NOT_SHIPPED', 60000, 0, 'CNY', '2026-08-04T02:30:00Z', '{"source":"demo_seed","scenario":"high_amount_approval"}'::jsonb),
    ('order_acme_1001', 'tenant_acme', 'LD202608040001', 'customer_acme_1001', 'PAID', 'NOT_SHIPPED', 25900, 0, 'CNY', '2026-08-04T02:30:00Z', '{"source":"demo_seed"}'::jsonb)
ON CONFLICT (tenant_id, external_order_no) DO UPDATE SET
    customer_id = EXCLUDED.customer_id,
    payment_status = EXCLUDED.payment_status,
    fulfillment_status = EXCLUDED.fulfillment_status,
    paid_amount = EXCLUDED.paid_amount,
    refunded_amount = EXCLUDED.refunded_amount,
    currency = EXCLUDED.currency,
    paid_at = EXCLUDED.paid_at,
    raw_snapshot = EXCLUDED.raw_snapshot;
