DROP INDEX IF EXISTS auth_sessions_user_tenant_idx;
DROP INDEX IF EXISTS auth_sessions_expires_at_idx;
DROP TABLE IF EXISTS auth_sessions;
DROP INDEX IF EXISTS users_email_normalized_unique_idx;
