package domain

import "time"

type ApprovalStatus string

const (
	ApprovalStatusPending  ApprovalStatus = "PENDING"
	ApprovalStatusApproved ApprovalStatus = "APPROVED"
	ApprovalStatusRejected ApprovalStatus = "REJECTED"
)

// 独立于退款申请，便于普通退款和高金额退款的多级审核都能被审计。
type Approval struct {
	ID              string
	RefundRequestID string
	Level           int // 审批级别
	Status          ApprovalStatus
	AssigneeID      string // 指定审批人
	DecisionBy      string // 实际操作人
	// DecisionAt 在审批完成前保持 nil，便于区分待处理和已处理任务。
	DecisionAt *time.Time
	Comment    string // 审批意见
}
