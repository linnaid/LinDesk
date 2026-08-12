package domain

import "time"

type RefundTransactionStatus string

const (
	RefundTransactionStatusSucceeded RefundTransactionStatus = "SUCCEEDED"
	RefundTransactionStatusFailed    RefundTransactionStatus = "FAILED"
)

// RefundTransaction 记录财务人工在支付渠道执行退款后的回执。
type RefundTransaction struct {
	ID               string
	TenantID         string
	RefundRequestID  string
	Provider         string // 支付渠道
	ProviderRefundNo string
	Amount           int64
	Status           RefundTransactionStatus
	FailureReason    string
	ProcessedBy      string
	ProcessedAt      time.Time
}
