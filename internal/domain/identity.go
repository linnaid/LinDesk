package domain

import "time"

// 租户状态
type TenantStatus string

const (
	TenantStatusActive   TenantStatus = "ACTIVE"
	TenantStatusDisabled TenantStatus = "DISABLED"
)

// 用户状态
type UserStatus string

const (
	UserStatusActive   UserStatus = "ACTIVE"
	UserStatusDisabled UserStatus = "DISABLED"
)

type RoleCode string

const (
	RoleCustomerService RoleCode = "CUSTOMER_SERVICE"
	RoleSupervisor      RoleCode = "SUPERVISOR"
	RoleFinance         RoleCode = "FINANCE"
	RoleWarehouse       RoleCode = "WAREHOUSE"
	RoleOperatorAdmin   RoleCode = "OPERATOR_ADMIN"
	RoleTenantAdmin     RoleCode = "TENANT_ADMIN"
)

// 权限
type Permission string

const (
	PermissionOrderRead              Permission = "order:read"
	PermissionRefundRequestCreate    Permission = "refund_request:create"
	PermissionRefundRequestRead      Permission = "refund_request:read"
	PermissionRefundRequestReview    Permission = "refund_request:review"
	PermissionRefundTransactionWrite Permission = "refund_transaction:write"
	PermissionTenantMemberManage     Permission = "tenant_member:manage"
)

type Tenant struct {
	ID        string
	Name      string
	Status    TenantStatus
	CreatedAt time.Time
}

type User struct {
	ID           string
	Name         string
	Email        string
	PasswordHash string
	Status       UserStatus
	CreatedAt    time.Time
}

type Role struct {
	Code        RoleCode
	Name        string
	Permissions []Permission
}

type TenantMember struct {
	TenantID string
	UserID   string
	RoleCode RoleCode
	JoinedAt time.Time
}

type Actor struct {
	Tenant Tenant
	User   User
	Roles  []Role
}

// 是否拥有某个权限
func (actor Actor) HasPermission(permission Permission) bool {
	for _, role := range actor.Roles {
		for _, rolePermission := range role.Permissions {
			if rolePermission == permission {
				return true
			}
		}
	}

	return false
}
