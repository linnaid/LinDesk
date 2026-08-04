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
