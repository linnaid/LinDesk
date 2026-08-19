-- 按小写邮箱保证登录查询不会因大小写产生重复账号。
CREATE UNIQUE INDEX users_email_normalized_unique_idx
ON users (lower(email));

-- Session 只保存 token hash；租户成员删除时级联清理对应登录态。
CREATE TABLE auth_sessions (
    id TEXT PRIMARY KEY,
    token_hash TEXT NOT NULL UNIQUE,
    tenant_id TEXT NOT NULL,
    user_id TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_id, user_id) REFERENCES tenant_members(tenant_id, user_id) ON DELETE CASCADE
);

-- 便于后续清理过期 Session 和查询用户的登录态。
CREATE INDEX auth_sessions_expires_at_idx ON auth_sessions (expires_at);
CREATE INDEX auth_sessions_user_tenant_idx ON auth_sessions (user_id, tenant_id);
