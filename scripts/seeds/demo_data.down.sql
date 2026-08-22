-- 删除本地演示数据。主要用于重置开发库，请勿在生产库执行。

DO $$
BEGIN
    IF to_regclass('public.audit_logs') IS NOT NULL THEN
        DELETE FROM audit_logs WHERE tenant_id IN ('tenant_demo', 'tenant_acme');
    END IF;
    IF to_regclass('public.refund_transactions') IS NOT NULL THEN
        DELETE FROM refund_transactions WHERE tenant_id IN ('tenant_demo', 'tenant_acme');
    END IF;
    IF to_regclass('public.approvals') IS NOT NULL THEN
        DELETE FROM approvals WHERE tenant_id IN ('tenant_demo', 'tenant_acme');
    END IF;
    IF to_regclass('public.refund_requests') IS NOT NULL THEN
        DELETE FROM refund_requests WHERE tenant_id IN ('tenant_demo', 'tenant_acme');
    END IF;
    IF to_regclass('public.orders') IS NOT NULL THEN
        DELETE FROM orders WHERE tenant_id IN ('tenant_demo', 'tenant_acme');
    END IF;
    IF to_regclass('public.tenant_members') IS NOT NULL THEN
        DELETE FROM tenant_members WHERE tenant_id IN ('tenant_demo', 'tenant_acme');
    END IF;
    IF to_regclass('public.users') IS NOT NULL THEN
        DELETE FROM users WHERE id IN (
            'user_cs_001',
            'user_supervisor_001',
            'user_finance_001',
            'user_finance_supervisor_001',
            'user_admin_001',
            'user_acme_cs_001',
            'user_acme_supervisor_001',
            'user_acme_finance_001',
            'user_acme_finance_supervisor_001'
        );
    END IF;
    IF to_regclass('public.roles') IS NOT NULL THEN
        DELETE FROM roles WHERE code IN ('CUSTOMER_SERVICE', 'SUPERVISOR', 'FINANCE', 'FINANCE_SUPERVISOR', 'TENANT_ADMIN');
    END IF;
    IF to_regclass('public.tenants') IS NOT NULL THEN
        DELETE FROM tenants WHERE id IN ('tenant_demo', 'tenant_acme');
    END IF;
END $$;
