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
	RoleCustomerService   RoleCode = "CUSTOMER_SERVICE"		// 客服
	RoleSupervisor        RoleCode = "SUPERVISOR"			// 主管
	RoleFinance           RoleCode = "FINANCE"				// 财务
	RoleFinanceSupervisor RoleCode = "FINANCE_SUPERVISOR"	// 财务主管
	RoleWarehouse         RoleCode = "WAREHOUSE"			// 仓储人员
	RoleOperatorAdmin     RoleCode = "OPERATOR_ADMIN"		// 运营管理员
	RoleTenantAdmin       RoleCode = "TENANT_ADMIN"			// 租户管理员
)

// 权限
type Permission string

const (
	PermissionOrderRead                     Permission = "order:read"
	PermissionRefundRequestCreate           Permission = "refund_request:create"
	PermissionRefundRequestRead             Permission = "refund_request:read"
	PermissionRefundRequestReview           Permission = "refund_request:review"
	PermissionRefundRequestHighAmountReview Permission = "refund_request:high_amount_review"
	PermissionRefundTransactionWrite        Permission = "refund_transaction:write"
	PermissionTenantMemberManage            Permission = "tenant_member:manage"
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
