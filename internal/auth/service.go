package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"lindesk/internal/domain"
)

// 一组认证/授权错误
var (
	ErrEmailRequired       = errors.New("email is required")
	ErrPasswordRequired    = errors.New("password is required")
	ErrInvalidCredentials  = errors.New("email or password is invalid") // 用户名密码错误
	ErrTenantRequired      = errors.New("tenant is required")           // 租户未指定
	ErrTenantNotFound      = errors.New("tenant not found")
	ErrTokenRequired       = errors.New("authorization token is required")
	ErrInvalidToken        = errors.New("authorization token is invalid")
	ErrSessionExpired      = errors.New("authorization session is expired")	// Session 已过期
	ErrPermissionDenied    = errors.New("permission denied")
	ErrNoActiveMembership  = errors.New("user has no active tenant membership") // 没有租户关系
	ErrAmbiguousMembership = errors.New("user belongs to multiple tenants")     // 多租户歧义
)

type LoginCommand struct {
	Email    string
	Password string
	TenantID string
}

// 一个已经登陆成功的用户会话
type Session struct {
	Token     string // 身份凭证 只在登录响应中返回，不持久化到服务端 session map
	Actor     domain.Actor
	ExpiresAt time.Time // 过期时间
}

type storedSession struct {
	Actor     domain.Actor
	ExpiresAt time.Time
}

// Service 是当前阶段的身份服务，负责登录、解析 token 和权限判断。
// 现在使用内存数据，后续可替换为 PostgreSQL + JWT。
type Service struct {
	mutex       sync.RWMutex
	tenantByID  map[string]domain.Tenant
	userByID    map[string]domain.User
	userByEmail map[string]domain.User
	roleByCode  map[domain.RoleCode]domain.Role // 角色表
	memberships []domain.TenantMember           // 用户属于哪些租户
	sessions    map[string]storedSession        // 按 token hash 保存登陆状态
	now         func() time.Time
}

func NewService(tenants []domain.Tenant, users []domain.User, roles []domain.Role, memberships []domain.TenantMember, now func() time.Time) *Service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}

	service := &Service{
		tenantByID:  make(map[string]domain.Tenant, len(tenants)),
		userByID:    make(map[string]domain.User, len(users)),
		userByEmail: make(map[string]domain.User, len(users)),
		roleByCode:  make(map[domain.RoleCode]domain.Role, len(roles)),
		memberships: append([]domain.TenantMember(nil), memberships...),
		sessions:    make(map[string]storedSession),
		now:         now,
	}

	for _, tenant := range tenants {
		service.tenantByID[tenant.ID] = tenant
	}
	for _, user := range users {
		service.userByID[user.ID] = user
		service.userByEmail[strings.ToLower(user.Email)] = user
	}
	for _, role := range roles {
		service.roleByCode[role.Code] = role
	}

	return service
}

func NewDemoService() *Service {
	now := time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC)

	return NewService(DemoTenants(now), DemoUsers(now), DemoRoles(), DemoMemberships(now), func() time.Time {
		return time.Now().UTC()
	})
}

func (service *Service) Login(_ context.Context, command LoginCommand) (Session, error) {
	command.Email = strings.TrimSpace(strings.ToLower(command.Email))
	command.Password = strings.TrimSpace(command.Password)
	command.TenantID = strings.TrimSpace(command.TenantID)

	if command.Email == "" {
		return Session{}, ErrEmailRequired
	}
	if command.Password == "" {
		return Session{}, ErrPasswordRequired
	}

	service.mutex.Lock()
	defer service.mutex.Unlock()

	user, ok := service.userByEmail[command.Email]
	if !ok || user.Status != domain.UserStatusActive || user.PasswordHash != HashPassword(command.Password) {
		return Session{}, ErrInvalidCredentials
	}

	actor, err := service.actorForUserLocked(user, command.TenantID)
	if err != nil {
		return Session{}, err
	}

	token, err := NewAccessToken()
	if err != nil {
		return Session{}, err
	}
	expiresAt := service.now().Add(8 * time.Hour)
	session := Session{
		Token:     token,
		Actor:     actor,
		ExpiresAt: expiresAt,
	}
	service.sessions[TokenHash(token)] = storedSession{
		Actor:     actor,
		ExpiresAt: expiresAt,
	}

	return session, nil
}

// 验证客户端传进来的Token是否有效，有效则返回对应用户身份
func (service *Service) Authenticate(_ context.Context, token string) (domain.Actor, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return domain.Actor{}, ErrTokenRequired
	}

	service.mutex.RLock()
	defer service.mutex.RUnlock()

	session, ok := service.sessions[TokenHash(token)]
	if !ok {
		return domain.Actor{}, ErrInvalidToken
	}
	if service.now().After(session.ExpiresAt) {
		return domain.Actor{}, ErrSessionExpired
	}

	return session.Actor, nil
}

func (service *Service) Logout(_ context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return ErrTokenRequired
	}

	service.mutex.Lock()
	defer service.mutex.Unlock()

	delete(service.sessions, TokenHash(token))
	return nil
}

// 检查当前用户是否拥有执行某个操作的权限
func RequirePermission(actor domain.Actor, permission domain.Permission) error {
	if !actor.HasPermission(permission) {
		return fmt.Errorf("%w: %s", ErrPermissionDenied, permission)
	}

	return nil
}

// 根据用户和tenantID找出用户当前所在的SaaS租户并加载该租户下的角色权限，组装成Actor
func (service *Service) actorForUserLocked(user domain.User, tenantID string) (domain.Actor, error) {
	memberships := make([]domain.TenantMember, 0)
	for _, membership := range service.memberships {
		if membership.UserID != user.ID {
			continue
		}
		if tenantID != "" && membership.TenantID != tenantID {
			continue
		}
		memberships = append(memberships, membership)
	}

	if len(memberships) == 0 {
		if tenantID != "" {
			return domain.Actor{}, ErrTenantNotFound
		}
		return domain.Actor{}, ErrNoActiveMembership
	}
	// 用户登陆时没传 tenant_id，且用户属于多租户，返回租户身份不明确
	if tenantID == "" && hasMultipleTenants(memberships) {
		return domain.Actor{}, ErrAmbiguousMembership
	}

	tenant, ok := service.tenantByID[memberships[0].TenantID]
	if !ok || tenant.Status != domain.TenantStatusActive {
		return domain.Actor{}, ErrTenantNotFound
	}

	roles := make([]domain.Role, 0, len(memberships))
	for _, membership := range memberships {
		role, ok := service.roleByCode[membership.RoleCode]
		if ok {
			roles = append(roles, role)
		}
	}

	return domain.Actor{Tenant: tenant, User: user, Roles: roles}, nil
}

// 用户的 membership 列表里是否属于多个不同的租户
func hasMultipleTenants(memberships []domain.TenantMember) bool {
	if len(memberships) <= 1 {
		return false
	}

	firstTenantID := memberships[0].TenantID
	for _, membership := range memberships[1:] {
		if membership.TenantID != firstTenantID {
			return true
		}
	}

	return false
}

// 后续修改点--------------------------
// 目前将密码经过 SHA-256 哈希处理，不是生产级别密码方案
func HashPassword(password string) string {
	sum := sha256.Sum256([]byte("lindesk-demo:" + password))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func NewAccessToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

// 返回 token 的 SHA-256哈希
func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func DemoTenants(now time.Time) []domain.Tenant {
	return []domain.Tenant{
		{ID: "tenant_demo", Name: "LinDesk Demo 电商", Status: domain.TenantStatusActive, CreatedAt: now},
		{ID: "tenant_acme", Name: "Acme 零售旗舰店", Status: domain.TenantStatusActive, CreatedAt: now},
	}
}

func DemoUsers(now time.Time) []domain.User {
	passwordHash := HashPassword("password123")

	return []domain.User{
		{ID: "user_cs_001", Name: "客服一号", Email: "cs@lindesk.local", PasswordHash: passwordHash, Status: domain.UserStatusActive, CreatedAt: now},
		{ID: "user_supervisor_001", Name: "主管一号", Email: "supervisor@lindesk.local", PasswordHash: passwordHash, Status: domain.UserStatusActive, CreatedAt: now},
		{ID: "user_finance_001", Name: "财务一号", Email: "finance@lindesk.local", PasswordHash: passwordHash, Status: domain.UserStatusActive, CreatedAt: now},
		{ID: "user_admin_001", Name: "管理员一号", Email: "admin@lindesk.local", PasswordHash: passwordHash, Status: domain.UserStatusActive, CreatedAt: now},
		{ID: "user_acme_cs_001", Name: "Acme 客服一号", Email: "acme.cs@lindesk.local", PasswordHash: passwordHash, Status: domain.UserStatusActive, CreatedAt: now},
		{ID: "user_acme_supervisor_001", Name: "Acme 主管一号", Email: "acme.supervisor@lindesk.local", PasswordHash: passwordHash, Status: domain.UserStatusActive, CreatedAt: now},
		{ID: "user_acme_finance_001", Name: "Acme 财务一号", Email: "acme.finance@lindesk.local", PasswordHash: passwordHash, Status: domain.UserStatusActive, CreatedAt: now},
	}
}

func DemoRoles() []domain.Role {
	return []domain.Role{
		{
			Code: domain.RoleCustomerService,
			Name: "客服专员",
			Permissions: []domain.Permission{
				domain.PermissionOrderRead,
				domain.PermissionRefundRequestCreate,
				domain.PermissionRefundRequestRead,
			},
		},
		{
			Code: domain.RoleSupervisor,
			Name: "客服主管",
			Permissions: []domain.Permission{
				domain.PermissionOrderRead,
				domain.PermissionRefundRequestRead,
				domain.PermissionRefundRequestReview,
			},
		},
		{
			Code: domain.RoleFinance,
			Name: "财务人员",
			Permissions: []domain.Permission{
				domain.PermissionRefundRequestRead,
				domain.PermissionRefundTransactionWrite,
			},
		},
		{
			Code: domain.RoleTenantAdmin,
			Name: "企业管理员",
			Permissions: []domain.Permission{
				domain.PermissionOrderRead,
				domain.PermissionRefundRequestCreate,
				domain.PermissionRefundRequestRead,
				domain.PermissionRefundRequestReview,
				domain.PermissionRefundTransactionWrite,
				domain.PermissionTenantMemberManage,
			},
		},
	}
}

func DemoMemberships(now time.Time) []domain.TenantMember {
	return []domain.TenantMember{
		{TenantID: "tenant_demo", UserID: "user_cs_001", RoleCode: domain.RoleCustomerService, JoinedAt: now},
		{TenantID: "tenant_demo", UserID: "user_supervisor_001", RoleCode: domain.RoleSupervisor, JoinedAt: now},
		{TenantID: "tenant_demo", UserID: "user_finance_001", RoleCode: domain.RoleFinance, JoinedAt: now},
		{TenantID: "tenant_demo", UserID: "user_admin_001", RoleCode: domain.RoleTenantAdmin, JoinedAt: now},
		{TenantID: "tenant_acme", UserID: "user_acme_cs_001", RoleCode: domain.RoleCustomerService, JoinedAt: now},
		{TenantID: "tenant_acme", UserID: "user_acme_supervisor_001", RoleCode: domain.RoleSupervisor, JoinedAt: now},
		{TenantID: "tenant_acme", UserID: "user_acme_finance_001", RoleCode: domain.RoleFinance, JoinedAt: now},
	}
}
