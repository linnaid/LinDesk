package domain

import "time"

type PaymentStatus string

const (
	PaymentStatusPending PaymentStatus = "PENDING"
	PaymentStatusPaid    PaymentStatus = "PAID"
)

type FulfillmentStatus string

const (
	FulfillmentStatusNotShipped FulfillmentStatus = "NOT_SHIPPED" // 已付款，未发货
	FulfillmentStatusShipped    FulfillmentStatus = "SHIPPED"     // 已发货
	FulfillmentStatusDelivered  FulfillmentStatus = "DELIVERED"   // 已送达
	FulfillmentStatusUnknown    FulfillmentStatus = "UNKNOWN"     // 未知状态
)

// 用于判断客户能否申请未发货退款的不可变订单业务快照。
type Order struct {
	ID                string
	TenantID          string
	ExternalOrderNo   string // 外部订单号
	CustomerID        string
	PaymentStatus     PaymentStatus
	FulfillmentStatus FulfillmentStatus
	PaidAmount        int64
	RefundedAmount    int64 // 已退款金额
	Currency          string
	PaidAt            time.Time
}

func (order Order) RefundableAmount() int64 {
	refundableAmount := order.PaidAmount - order.RefundedAmount
	if refundableAmount < 0 {
		return 0
	}
	return refundableAmount
}

func (order Order) CanRequestUnshippedRefund() bool {
	return order.PaymentStatus == PaymentStatusPaid &&
		order.FulfillmentStatus == FulfillmentStatusNotShipped &&
		order.RefundableAmount() > 0
}
