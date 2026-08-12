-- 回滚顺序必须先删子表和索引，再删被引用的父表。

DROP INDEX IF EXISTS audit_logs_entity_idx;
DROP INDEX IF EXISTS audit_logs_tenant_id_idx;
DROP INDEX IF EXISTS refund_transactions_tenant_id_idx;
DROP INDEX IF EXISTS approvals_tenant_id_idx;
DROP INDEX IF EXISTS refund_requests_tenant_id_idx;
DROP INDEX IF EXISTS orders_tenant_id_idx;
DROP INDEX IF EXISTS refund_transactions_provider_refund_no_unique_idx;

DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS refund_transactions;
DROP TABLE IF EXISTS approvals;
DROP TABLE IF EXISTS refund_requests;
DROP TABLE IF EXISTS orders;
DROP TABLE IF EXISTS tenant_members;
DROP TABLE IF EXISTS roles;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS tenants;
