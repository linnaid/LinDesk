package domain

import "time"

// AuditLog 记录业务对象的不可变操作轨迹，后续落库时会写入 audit_logs 表。
type AuditLog struct {
	ID         string
	EntityType string
	EntityID   string
	Action     string
	OperatorID string
	BeforeData map[string]any
	AfterData  map[string]any
	CreatedAt  time.Time
}
