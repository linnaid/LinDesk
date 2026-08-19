// 每次鉴权动态读取最新租户成员以及角色权限
package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"lindesk/internal/domain"
)

// PostgresRepository 将 Auth 数据映射到 users、tenant_members、roles 和 auth_sessions。
type PostgresRepository struct {
	db *sql.DB
}

var _ Repository = (*PostgresRepository)(nil)

func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	if db == nil {
		panic("postgres database handle is required")
	}

	return &PostgresRepository{db: db}
}

func NewPostgresService(db *sql.DB) *PersistentService {
	return NewPersistentService(NewPostgresRepository(db), nil)
}

func (repository *PostgresRepository) FindUserByEmail(ctx context.Context, email string) (domain.User, error) {
	var user domain.User
	var status string
	// 邮箱按小写比较，与 users_email_normalized_unique_idx 保持一致。
	err := repository.db.QueryRowContext(ctx, `
SELECT id, name, email, password_hash, status, created_at
FROM users
WHERE lower(email) = lower($1)
`, email).Scan(&user.ID, &user.Name, &user.Email, &user.PasswordHash, &status, &user.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.User{}, errUserNotFound
	}
	if err != nil {
		return domain.User{}, err
	}

	user.Status = domain.UserStatus(status)
	return user, nil
}

func (repository *PostgresRepository) ResolveActor(ctx context.Context, userID, tenantID string) (domain.Actor, error) {
	// 每次调用都从数据库读取成员和角色，避免把登录时的旧权限快照长期缓存。
	rows, err := repository.db.QueryContext(ctx, `
SELECT t.id, t.name, t.status, t.created_at,
       u.id, u.name, u.email, u.password_hash, u.status, u.created_at,
       r.code, r.name, r.permissions
FROM users u
JOIN tenant_members tm ON tm.user_id = u.id
JOIN tenants t ON t.id = tm.tenant_id
JOIN roles r ON r.code = tm.role_code
WHERE u.id = $1
  AND u.status = 'ACTIVE'
  AND t.status = 'ACTIVE'
  AND ($2 = '' OR tm.tenant_id = $2)
ORDER BY t.id, r.code
`, userID, tenantID)
	if err != nil {
		return domain.Actor{}, err
	}
	defer rows.Close()

	var actor domain.Actor
	selectedTenantID := ""
	for rows.Next() {
		var tenant domain.Tenant
		var user domain.User
		var role domain.Role
		var tenantStatus string
		var userStatus string
		var roleCode string
		var permissionsJSON []byte
		if err := rows.Scan(
			&tenant.ID,
			&tenant.Name,
			&tenantStatus,
			&tenant.CreatedAt,
			&user.ID,
			&user.Name,
			&user.Email,
			&user.PasswordHash,
			&userStatus,
			&user.CreatedAt,
			&roleCode,
			&role.Name,
			&permissionsJSON,
		); err != nil {
			return domain.Actor{}, err
		}

		// 未指定租户时，一个用户属于多个租户必须明确拒绝，避免身份歧义。
		if selectedTenantID != "" && selectedTenantID != tenant.ID {
			return domain.Actor{}, ErrAmbiguousMembership
		}
		selectedTenantID = tenant.ID
		tenant.Status = domain.TenantStatus(tenantStatus)
		user.Status = domain.UserStatus(userStatus)
		role.Code = domain.RoleCode(roleCode)
		if err := json.Unmarshal(permissionsJSON, &role.Permissions); err != nil {
			return domain.Actor{}, fmt.Errorf("decode permissions for role %q: %w", role.Code, err)
		}

		actor.Tenant = tenant
		actor.User = user
		actor.Roles = append(actor.Roles, role)
	}
	if err := rows.Err(); err != nil {
		return domain.Actor{}, err
	}
	if selectedTenantID == "" {
		if tenantID != "" {
			return domain.Actor{}, ErrTenantNotFound
		}
		return domain.Actor{}, ErrNoActiveMembership
	}

	return actor, nil
}

func (repository *PostgresRepository) CreateSession(ctx context.Context, session SessionRecord) error {
	// token_hash 是唯一键，既防止重复写入，也避免数据库保存原始 Token。
	_, err := repository.db.ExecContext(ctx, `
INSERT INTO auth_sessions (
    id, token_hash, tenant_id, user_id, expires_at, revoked_at, created_at
) VALUES ($1, $2, $3, $4, $5, $6, $7)
`, session.ID, session.TokenHash, session.TenantID, session.UserID, session.ExpiresAt, session.RevokedAt, session.CreatedAt)
	return err
}

func (repository *PostgresRepository) FindSessionByTokenHash(ctx context.Context, tokenHash string) (SessionRecord, error) {
	var session SessionRecord
	var revokedAt sql.NullTime
	err := repository.db.QueryRowContext(ctx, `
SELECT id, token_hash, tenant_id, user_id, expires_at, revoked_at, created_at
FROM auth_sessions
WHERE token_hash = $1
`, tokenHash).Scan(
		&session.ID,
		&session.TokenHash,
		&session.TenantID,
		&session.UserID,
		&session.ExpiresAt,
		&revokedAt,
		&session.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		// 对外统一转换为无效 Token，避免暴露 Session 是否存在。
		return SessionRecord{}, errSessionNotFound
	}
	if err != nil {
		return SessionRecord{}, err
	}
	if revokedAt.Valid {
		revoked := revokedAt.Time
		session.RevokedAt = &revoked
	}

	return session, nil
}

func (repository *PostgresRepository) RevokeSession(ctx context.Context, tokenHash string, revokedAt time.Time) error {
	// COALESCE 保留首次注销时间，使重复注销不会覆盖原始审计时间。
	_, err := repository.db.ExecContext(ctx, `
UPDATE auth_sessions
SET revoked_at = COALESCE(revoked_at, $1)
WHERE token_hash = $2
`, revokedAt, tokenHash)
	return err
}
