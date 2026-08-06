package domain

import "time"

type RefundRequestStatus string

const (
	RefundRequestStatusDraft         RefundRequestStatus = "DRAFT"
	RefundRequestStatusPendingReview RefundRequestStatus = "PENDING_REVIEW"
	RefundRequestStatusApproved      RefundRequestStatus = "APPROVED"
	RefundRequestStatusProcessing    RefundRequestStatus = "PROCESSING"
	RefundRequestStatusSucceeded     RefundRequestStatus = "SUCCEEDED"
	RefundRequestStatusRejected      RefundRequestStatus = "REJECTED"
	RefundRequestStatusFailed        RefundRequestStatus = "FAILED"
	RefundRequestStatusCancelled     RefundRequestStatus = "CANCELLED"
)

// 记录由客服发起的退款申请。金额统一使用货币最小单位，例如人民币分。
type RefundRequest struct {
	ID        string
	RequestNo string // 退款申请编号
	OrderID   string
	// OrderSnapshot 固化提交退款时的订单状态，后续审核和追溯都以这份快照为准。
	OrderSnapshot   Order
	RequestedAmount int64
	ReasonCode      string // 退款原因代码
	ReasonNote      string // 退款备注
	Status          RefundRequestStatus
	SubmittedBy     string
	SubmittedAt     time.Time
}

// 这笔退款是否达到高金额审批标准
func (request RefundRequest) RequiresHighAmountApproval(threshold int64) bool {
	return request.RequestedAmount >= threshold
}
