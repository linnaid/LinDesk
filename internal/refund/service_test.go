package refund

import (
	"context"
	"errors"
	"testing"
	"time"

	"lindesk/internal/domain"
)

type fixedClock struct {
	now time.Time
}

func (clock fixedClock) Now() time.Time {
	return clock.now
}

func TestCreateRequestCreatesPendingReviewRequest(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())

	detail, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		ReasonNote:      "客户取消未发货订单",
		SubmittedBy:     "user_cs_001",
	})
	if err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}

	request := detail.Request
	if request.RequestNo != "RR202608040001" {
		t.Fatalf("RequestNo = %q, want RR202608040001", request.RequestNo)
	}
	if request.Status != domain.RefundRequestStatusPendingReview {
		t.Fatalf("Status = %q, want %q", request.Status, domain.RefundRequestStatusPendingReview)
	}
	if request.OrderSnapshot.ExternalOrderNo != "LD202608040001" {
		t.Fatalf("OrderSnapshot.ExternalOrderNo = %q", request.OrderSnapshot.ExternalOrderNo)
	}
	if detail.RequiresHighAmountApproval {
		t.Fatalf("RequiresHighAmountApproval = true, want false")
	}
	if len(repository.AuditLogs()) != 1 {
		t.Fatalf("AuditLogs length = %d, want 1", len(repository.AuditLogs()))
	}
}

func TestCreateRequestMarksHighAmountRequest(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository([]domain.Order{
		{
			ID:                "order_high",
			ExternalOrderNo:   "LD202608040099",
			CustomerID:        "customer_high",
			PaymentStatus:     domain.PaymentStatusPaid,
			FulfillmentStatus: domain.FulfillmentStatusNotShipped,
			PaidAmount:        60_000,
			Currency:          "CNY",
			PaidAt:            now,
		},
	})
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())

	detail, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		ExternalOrderNo: "LD202608040099",
		RequestedAmount: 50_000,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	})
	if err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}
	if !detail.RequiresHighAmountApproval {
		t.Fatalf("RequiresHighAmountApproval = false, want true")
	}
}

func TestCreateRequestRejectsIneligibleOrder(t *testing.T) {
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: time.Now()}, NewSequentialRequestNumberGenerator())

	_, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		ExternalOrderNo: "LD202608040002",
		RequestedAmount: 8_800,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	})
	if !errors.Is(err, ErrOrderNotRefundable) {
		t.Fatalf("CreateRequest() error = %v, want %v", err, ErrOrderNotRefundable)
	}
}

func TestCreateRequestRejectsAmountAboveRefundableBalance(t *testing.T) {
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: time.Now()}, NewSequentialRequestNumberGenerator())

	_, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_901,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	})
	if !errors.Is(err, ErrAmountExceedsRefundable) {
		t.Fatalf("CreateRequest() error = %v, want %v", err, ErrAmountExceedsRefundable)
	}
}

func TestCreateRequestRejectsActiveRequestForSameOrder(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())
	command := CreateRequestCommand{
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}

	if _, err := service.CreateRequest(context.Background(), command); err != nil {
		t.Fatalf("first CreateRequest() error = %v", err)
	}
	_, err := service.CreateRequest(context.Background(), command)
	if !errors.Is(err, ErrActiveRefundRequestExists) {
		t.Fatalf("second CreateRequest() error = %v, want %v", err, ErrActiveRefundRequestExists)
	}
}

func TestApproveRequestTransitionsToApprovedAndStoresApproval(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())
	if _, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}

	result, err := service.ApproveRequest(context.Background(), ReviewRequestCommand{
		RequestNo:  "RR202608040001",
		DecisionBy: "user_supervisor_001",
		Comment:    "订单未发货，符合退款规则",
	})
	if err != nil {
		t.Fatalf("ApproveRequest() error = %v", err)
	}
	if result.Request.Request.Status != domain.RefundRequestStatusApproved {
		t.Fatalf("Status = %q, want %q", result.Request.Request.Status, domain.RefundRequestStatusApproved)
	}
	if result.Approval.Status != domain.ApprovalStatusApproved {
		t.Fatalf("Approval status = %q, want %q", result.Approval.Status, domain.ApprovalStatusApproved)
	}
	if result.Approval.DecisionAt == nil {
		t.Fatalf("DecisionAt is nil, want time")
	}
	if len(result.Request.Approvals) != 1 {
		t.Fatalf("Approvals length = %d, want 1", len(result.Request.Approvals))
	}
	if result.Request.Approvals[0].Comment != "订单未发货，符合退款规则" {
		t.Fatalf("stored approval comment = %q", result.Request.Approvals[0].Comment)
	}
}

func TestApproveRequestRejectsSubmitterSelfApproval(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())
	if _, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}

	_, err := service.ApproveRequest(context.Background(), ReviewRequestCommand{
		RequestNo:  "RR202608040001",
		DecisionBy: "user_cs_001",
		Comment:    "不应通过",
	})
	if !errors.Is(err, ErrApprovalActorSameAsSubmitter) {
		t.Fatalf("ApproveRequest() error = %v, want %v", err, ErrApprovalActorSameAsSubmitter)
	}
}

func TestRejectRequestTransitionsToRejected(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())
	if _, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}

	result, err := service.RejectRequest(context.Background(), ReviewRequestCommand{
		RequestNo:  "RR202608040001",
		DecisionBy: "user_supervisor_001",
		Comment:    "客户信息不完整，驳回",
	})
	if err != nil {
		t.Fatalf("RejectRequest() error = %v", err)
	}
	if result.Request.Request.Status != domain.RefundRequestStatusRejected {
		t.Fatalf("Status = %q, want %q", result.Request.Request.Status, domain.RefundRequestStatusRejected)
	}
	if result.Approval.Status != domain.ApprovalStatusRejected {
		t.Fatalf("Approval status = %q, want %q", result.Approval.Status, domain.ApprovalStatusRejected)
	}
}

func TestRecordTransactionTransitionsApprovedRequestToSucceeded(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())
	if _, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}
	if _, err := service.ApproveRequest(context.Background(), ReviewRequestCommand{
		RequestNo:  "RR202608040001",
		DecisionBy: "user_supervisor_001",
		Comment:    "订单未发货，符合退款规则",
	}); err != nil {
		t.Fatalf("ApproveRequest() error = %v", err)
	}

	result, err := service.RecordTransaction(context.Background(), RecordTransactionCommand{
		RequestNo:        "RR202608040001",
		Provider:         "alipay",
		ProviderRefundNo: "ALI202608040001",
		Amount:           12_900,
		Status:           domain.RefundTransactionStatusSucceeded,
		ProcessedBy:      "user_finance_001",
	})
	if err != nil {
		t.Fatalf("RecordTransaction() error = %v", err)
	}
	if result.Request.Request.Status != domain.RefundRequestStatusSucceeded {
		t.Fatalf("Status = %q, want %q", result.Request.Request.Status, domain.RefundRequestStatusSucceeded)
	}
	if result.Transaction.Status != domain.RefundTransactionStatusSucceeded {
		t.Fatalf("Transaction status = %q, want %q", result.Transaction.Status, domain.RefundTransactionStatusSucceeded)
	}
	if result.Transaction.ProcessedAt.IsZero() {
		t.Fatalf("ProcessedAt is zero, want time")
	}
	if len(result.Request.RefundTransactions) != 1 {
		t.Fatalf("RefundTransactions length = %d, want 1", len(result.Request.RefundTransactions))
	}
	if len(repository.AuditLogs()) != 3 {
		t.Fatalf("AuditLogs length = %d, want 3", len(repository.AuditLogs()))
	}
}

func TestRecordTransactionTransitionsApprovedRequestToFailed(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())
	if _, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}
	if _, err := service.ApproveRequest(context.Background(), ReviewRequestCommand{
		RequestNo:  "RR202608040001",
		DecisionBy: "user_supervisor_001",
		Comment:    "订单未发货，符合退款规则",
	}); err != nil {
		t.Fatalf("ApproveRequest() error = %v", err)
	}

	result, err := service.RecordTransaction(context.Background(), RecordTransactionCommand{
		RequestNo:     "RR202608040001",
		Provider:      "alipay",
		Amount:        12_900,
		Status:        domain.RefundTransactionStatusFailed,
		FailureReason: "支付渠道返回余额不足",
		ProcessedBy:   "user_finance_001",
	})
	if err != nil {
		t.Fatalf("RecordTransaction() error = %v", err)
	}
	if result.Request.Request.Status != domain.RefundRequestStatusFailed {
		t.Fatalf("Status = %q, want %q", result.Request.Request.Status, domain.RefundRequestStatusFailed)
	}
	if result.Transaction.FailureReason != "支付渠道返回余额不足" {
		t.Fatalf("FailureReason = %q", result.Transaction.FailureReason)
	}
}

func TestRecordTransactionRejectsPendingReviewRequest(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())
	if _, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}

	_, err := service.RecordTransaction(context.Background(), RecordTransactionCommand{
		RequestNo:        "RR202608040001",
		Provider:         "alipay",
		ProviderRefundNo: "ALI202608040001",
		Amount:           12_900,
		Status:           domain.RefundTransactionStatusSucceeded,
		ProcessedBy:      "user_finance_001",
	})
	if !errors.Is(err, ErrRefundRequestNotApproved) {
		t.Fatalf("RecordTransaction() error = %v, want %v", err, ErrRefundRequestNotApproved)
	}
}

func TestRecordTransactionRejectsAmountMismatch(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())
	if _, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}
	if _, err := service.ApproveRequest(context.Background(), ReviewRequestCommand{
		RequestNo:  "RR202608040001",
		DecisionBy: "user_supervisor_001",
		Comment:    "订单未发货，符合退款规则",
	}); err != nil {
		t.Fatalf("ApproveRequest() error = %v", err)
	}

	_, err := service.RecordTransaction(context.Background(), RecordTransactionCommand{
		RequestNo:        "RR202608040001",
		Provider:         "alipay",
		ProviderRefundNo: "ALI202608040001",
		Amount:           12_899,
		Status:           domain.RefundTransactionStatusSucceeded,
		ProcessedBy:      "user_finance_001",
	})
	if !errors.Is(err, ErrTransactionAmountMismatch) {
		t.Fatalf("RecordTransaction() error = %v, want %v", err, ErrTransactionAmountMismatch)
	}
}
