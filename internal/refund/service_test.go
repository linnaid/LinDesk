package refund

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"lindesk/internal/domain"
)

type fixedClock struct {
	now time.Time
}

const (
	demoTenantID                  = "tenant_demo"
	acmeTenantID                  = "tenant_acme"
	testIdempotencyKey            = "test-refund-create"
	testTransactionIdempotencyKey = "test-refund-transaction"
)

func (clock fixedClock) Now() time.Time {
	return clock.now
}

func TestGetOrderScopesByTenant(t *testing.T) {
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: time.Now()}, NewSequentialRequestNumberGenerator())

	demoOrder, err := service.GetOrder(context.Background(), demoTenantID, "LD202608040001")
	if err != nil {
		t.Fatalf("demo GetOrder() error = %v", err)
	}
	acmeOrder, err := service.GetOrder(context.Background(), acmeTenantID, "LD202608040001")
	if err != nil {
		t.Fatalf("acme GetOrder() error = %v", err)
	}

	if demoOrder.TenantID != demoTenantID || demoOrder.RefundableAmount() != 12_900 {
		t.Fatalf("demo order = %+v", demoOrder)
	}
	if acmeOrder.TenantID != acmeTenantID || acmeOrder.RefundableAmount() != 25_900 {
		t.Fatalf("acme order = %+v", acmeOrder)
	}
}

func TestCreateRequestCreatesPendingReviewRequest(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())

	detail, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
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
	if request.TenantID != demoTenantID {
		t.Fatalf("TenantID = %q, want %q", request.TenantID, demoTenantID)
	}
	if request.Status != domain.RefundRequestStatusPendingReview {
		t.Fatalf("Status = %q, want %q", request.Status, domain.RefundRequestStatusPendingReview)
	}
	if request.OrderSnapshot.TenantID != demoTenantID {
		t.Fatalf("OrderSnapshot.TenantID = %q, want %q", request.OrderSnapshot.TenantID, demoTenantID)
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
	if repository.AuditLogs()[0].TenantID != demoTenantID {
		t.Fatalf("AuditLog.TenantID = %q, want %q", repository.AuditLogs()[0].TenantID, demoTenantID)
	}
}

func TestCreateRequestRequiresIdempotencyKey(t *testing.T) {
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: time.Now()}, NewSequentialRequestNumberGenerator())

	_, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		TenantID:        demoTenantID,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	})
	if !errors.Is(err, ErrIdempotencyKeyRequired) {
		t.Fatalf("CreateRequest() error = %v, want %v", err, ErrIdempotencyKeyRequired)
	}
}

func TestCreateRequestReplaysFirstResultForSameIdempotencyKey(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())
	command := CreateRequestCommand{
		TenantID:        demoTenantID,
		IdempotencyKey:  "refund-create-replay",
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		ReasonNote:      "客户取消未发货订单",
		SubmittedBy:     "user_cs_001",
	}

	first, err := service.CreateRequest(context.Background(), command)
	if err != nil {
		t.Fatalf("first CreateRequest() error = %v", err)
	}
	second, err := service.CreateRequest(context.Background(), command)
	if err != nil {
		t.Fatalf("second CreateRequest() error = %v", err)
	}

	if first.IdempotencyReplayed {
		t.Fatalf("first IdempotencyReplayed = true, want false")
	}
	if !second.IdempotencyReplayed {
		t.Fatalf("second IdempotencyReplayed = false, want true")
	}
	if second.Request.RequestNo != first.Request.RequestNo {
		t.Fatalf("request numbers = %q and %q, want same result", first.Request.RequestNo, second.Request.RequestNo)
	}
	if len(repository.AuditLogs()) != 1 {
		t.Fatalf("AuditLogs length = %d, want 1", len(repository.AuditLogs()))
	}
}

func TestCreateRequestReplayIgnoresLaterOrderStateChanges(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())
	command := CreateRequestCommand{
		TenantID:        demoTenantID,
		IdempotencyKey:  "refund-create-order-changed",
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}

	first, err := service.CreateRequest(context.Background(), command)
	if err != nil {
		t.Fatalf("first CreateRequest() error = %v", err)
	}
	repository.mutex.Lock()
	orderKey := tenantKey(demoTenantID, command.ExternalOrderNo)
	order := repository.orders[orderKey]
	order.FulfillmentStatus = domain.FulfillmentStatusShipped
	repository.orders[orderKey] = order
	repository.mutex.Unlock()

	second, err := service.CreateRequest(context.Background(), command)
	if err != nil {
		t.Fatalf("second CreateRequest() error = %v, want idempotent replay", err)
	}
	if !second.IdempotencyReplayed || second.Request.RequestNo != first.Request.RequestNo {
		t.Fatalf("second result = %+v, want replay of %q", second, first.Request.RequestNo)
	}
}

func TestCreateRequestRejectsIdempotencyKeyWithDifferentPayload(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())
	command := CreateRequestCommand{
		TenantID:        demoTenantID,
		IdempotencyKey:  "refund-create-conflict",
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}

	if _, err := service.CreateRequest(context.Background(), command); err != nil {
		t.Fatalf("first CreateRequest() error = %v", err)
	}
	command.RequestedAmount = 12_000
	_, err := service.CreateRequest(context.Background(), command)
	if !errors.Is(err, ErrIdempotencyKeyConflict) {
		t.Fatalf("second CreateRequest() error = %v, want %v", err, ErrIdempotencyKeyConflict)
	}
}

func TestCreateRequestConcurrentRetriesCreateOnce(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())
	command := CreateRequestCommand{
		TenantID:        demoTenantID,
		IdempotencyKey:  "refund-create-concurrent",
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}

	const workers = 16
	start := make(chan struct{})
	results := make(chan RequestDetail, workers)
	errorsByWorker := make(chan error, workers)
	var waitGroup sync.WaitGroup
	for range workers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			detail, err := service.CreateRequest(context.Background(), command)
			if err != nil {
				errorsByWorker <- err
				return
			}
			results <- detail
		}()
	}
	close(start)
	waitGroup.Wait()
	close(results)
	close(errorsByWorker)

	for err := range errorsByWorker {
		t.Fatalf("concurrent CreateRequest() error = %v", err)
	}
	requestNo := ""
	firstResultCount := 0
	resultCount := 0
	for detail := range results {
		resultCount++
		if requestNo == "" {
			requestNo = detail.Request.RequestNo
		}
		if detail.Request.RequestNo != requestNo {
			t.Fatalf("RequestNo = %q, want %q", detail.Request.RequestNo, requestNo)
		}
		if !detail.IdempotencyReplayed {
			firstResultCount++
		}
	}
	if resultCount != workers {
		t.Fatalf("result count = %d, want %d", resultCount, workers)
	}
	if firstResultCount != 1 {
		t.Fatalf("first result count = %d, want 1", firstResultCount)
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
			TenantID:          demoTenantID,
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
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
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
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
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
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
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
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}

	if _, err := service.CreateRequest(context.Background(), command); err != nil {
		t.Fatalf("first CreateRequest() error = %v", err)
	}
	command.IdempotencyKey = "test-refund-create-second"
	_, err := service.CreateRequest(context.Background(), command)
	if !errors.Is(err, ErrActiveRefundRequestExists) {
		t.Fatalf("second CreateRequest() error = %v, want %v", err, ErrActiveRefundRequestExists)
	}
}

func TestCreateRequestScopesActiveRequestsByTenant(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())

	demoDetail, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	})
	if err != nil {
		t.Fatalf("demo CreateRequest() error = %v", err)
	}

	acmeDetail, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		TenantID:        acmeTenantID,
		IdempotencyKey:  testIdempotencyKey,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 25_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_acme_cs_001",
	})
	if err != nil {
		t.Fatalf("acme CreateRequest() error = %v", err)
	}
	if demoDetail.Request.RequestNo != acmeDetail.Request.RequestNo {
		t.Fatalf("RequestNo = %q and %q, want same tenant-scoped number", demoDetail.Request.RequestNo, acmeDetail.Request.RequestNo)
	}
	if acmeDetail.Request.OrderSnapshot.PaidAmount != 25_900 {
		t.Fatalf("acme PaidAmount = %d, want 25900", acmeDetail.Request.OrderSnapshot.PaidAmount)
	}

	_, err = service.CreateRequest(context.Background(), CreateRequestCommand{
		TenantID:        demoTenantID,
		IdempotencyKey:  "test-refund-create-second",
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	})
	if !errors.Is(err, ErrActiveRefundRequestExists) {
		t.Fatalf("second demo CreateRequest() error = %v, want %v", err, ErrActiveRefundRequestExists)
	}
}

func TestGetRequestReturnsNotFoundAcrossTenant(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())

	detail, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	})
	if err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}

	_, err = service.GetRequest(context.Background(), acmeTenantID, detail.Request.RequestNo)
	if !errors.Is(err, ErrRefundRequestNotFound) {
		t.Fatalf("GetRequest() error = %v, want %v", err, ErrRefundRequestNotFound)
	}
}

func TestApproveRequestTransitionsToApprovedAndStoresApproval(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())
	if _, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}

	result, err := service.ApproveRequest(context.Background(), ReviewRequestCommand{
		TenantID:   demoTenantID,
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
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}

	_, err := service.ApproveRequest(context.Background(), ReviewRequestCommand{
		TenantID:   demoTenantID,
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
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}

	result, err := service.RejectRequest(context.Background(), ReviewRequestCommand{
		TenantID:   demoTenantID,
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
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}
	if _, err := service.ApproveRequest(context.Background(), ReviewRequestCommand{
		TenantID:   demoTenantID,
		RequestNo:  "RR202608040001",
		DecisionBy: "user_supervisor_001",
		Comment:    "订单未发货，符合退款规则",
	}); err != nil {
		t.Fatalf("ApproveRequest() error = %v", err)
	}

	result, err := service.RecordTransaction(context.Background(), RecordTransactionCommand{
		TenantID:         demoTenantID,
		IdempotencyKey:   testTransactionIdempotencyKey,
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

func TestRecordTransactionRequiresIdempotencyKey(t *testing.T) {
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: time.Now()}, NewSequentialRequestNumberGenerator())

	_, err := service.RecordTransaction(context.Background(), RecordTransactionCommand{
		TenantID:         demoTenantID,
		RequestNo:        "RR202608040001",
		Provider:         "alipay",
		ProviderRefundNo: "ALI202608040001",
		Amount:           12_900,
		Status:           domain.RefundTransactionStatusSucceeded,
		ProcessedBy:      "user_finance_001",
	})
	if !errors.Is(err, ErrIdempotencyKeyRequired) {
		t.Fatalf("RecordTransaction() error = %v, want %v", err, ErrIdempotencyKeyRequired)
	}
}

func TestRecordTransactionReplaysFirstResult(t *testing.T) {
	service, repository := newApprovedRefundService(t)
	command := RecordTransactionCommand{
		TenantID:         demoTenantID,
		IdempotencyKey:   "refund-transaction-replay",
		RequestNo:        "RR202608040001",
		Provider:         "alipay",
		ProviderRefundNo: "ALI-REPLAY-001",
		Amount:           12_900,
		Status:           domain.RefundTransactionStatusSucceeded,
		ProcessedBy:      "user_finance_001",
	}

	first, err := service.RecordTransaction(context.Background(), command)
	if err != nil {
		t.Fatalf("first RecordTransaction() error = %v", err)
	}
	second, err := service.RecordTransaction(context.Background(), command)
	if err != nil {
		t.Fatalf("second RecordTransaction() error = %v", err)
	}

	if first.IdempotencyReplayed {
		t.Fatalf("first IdempotencyReplayed = true, want false")
	}
	if !second.IdempotencyReplayed {
		t.Fatalf("second IdempotencyReplayed = false, want true")
	}
	if second.Transaction.ID != first.Transaction.ID {
		t.Fatalf("transaction IDs = %q and %q, want same", first.Transaction.ID, second.Transaction.ID)
	}
	if len(second.Request.RefundTransactions) != 1 {
		t.Fatalf("RefundTransactions length = %d, want 1", len(second.Request.RefundTransactions))
	}
	if len(repository.AuditLogs()) != 3 {
		t.Fatalf("AuditLogs length = %d, want 3", len(repository.AuditLogs()))
	}
}

func TestRecordTransactionRejectsIdempotencyKeyWithDifferentPayload(t *testing.T) {
	service, _ := newApprovedRefundService(t)
	command := RecordTransactionCommand{
		TenantID:         demoTenantID,
		IdempotencyKey:   "refund-transaction-conflict",
		RequestNo:        "RR202608040001",
		Provider:         "alipay",
		ProviderRefundNo: "ALI-CONFLICT-001",
		Amount:           12_900,
		Status:           domain.RefundTransactionStatusSucceeded,
		ProcessedBy:      "user_finance_001",
	}

	if _, err := service.RecordTransaction(context.Background(), command); err != nil {
		t.Fatalf("first RecordTransaction() error = %v", err)
	}
	command.ProviderRefundNo = "ALI-CONFLICT-002"
	_, err := service.RecordTransaction(context.Background(), command)
	if !errors.Is(err, ErrIdempotencyKeyConflict) {
		t.Fatalf("second RecordTransaction() error = %v, want %v", err, ErrIdempotencyKeyConflict)
	}
}

func TestRecordTransactionConcurrentRetriesWriteOnce(t *testing.T) {
	service, repository := newApprovedRefundService(t)
	command := RecordTransactionCommand{
		TenantID:         demoTenantID,
		IdempotencyKey:   "refund-transaction-concurrent",
		RequestNo:        "RR202608040001",
		Provider:         "alipay",
		ProviderRefundNo: "ALI-CONCURRENT-001",
		Amount:           12_900,
		Status:           domain.RefundTransactionStatusSucceeded,
		ProcessedBy:      "user_finance_001",
	}

	const workers = 16
	start := make(chan struct{})
	results := make(chan TransactionResult, workers)
	errorsByWorker := make(chan error, workers)
	var waitGroup sync.WaitGroup
	for range workers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			<-start
			result, err := service.RecordTransaction(context.Background(), command)
			if err != nil {
				errorsByWorker <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	waitGroup.Wait()
	close(results)
	close(errorsByWorker)

	for err := range errorsByWorker {
		t.Fatalf("concurrent RecordTransaction() error = %v", err)
	}
	firstResultCount := 0
	resultCount := 0
	transactionID := ""
	for result := range results {
		resultCount++
		if transactionID == "" {
			transactionID = result.Transaction.ID
		}
		if result.Transaction.ID != transactionID {
			t.Fatalf("Transaction.ID = %q, want %q", result.Transaction.ID, transactionID)
		}
		if !result.IdempotencyReplayed {
			firstResultCount++
		}
	}
	if resultCount != workers {
		t.Fatalf("result count = %d, want %d", resultCount, workers)
	}
	if firstResultCount != 1 {
		t.Fatalf("first result count = %d, want 1", firstResultCount)
	}
	requestKey := tenantKey(demoTenantID, command.RequestNo)
	if len(repository.transactions[requestKey]) != 1 {
		t.Fatalf("transaction count = %d, want 1", len(repository.transactions[requestKey]))
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
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}
	if _, err := service.ApproveRequest(context.Background(), ReviewRequestCommand{
		TenantID:   demoTenantID,
		RequestNo:  "RR202608040001",
		DecisionBy: "user_supervisor_001",
		Comment:    "订单未发货，符合退款规则",
	}); err != nil {
		t.Fatalf("ApproveRequest() error = %v", err)
	}

	result, err := service.RecordTransaction(context.Background(), RecordTransactionCommand{
		TenantID:       demoTenantID,
		IdempotencyKey: testTransactionIdempotencyKey,
		RequestNo:      "RR202608040001",
		Provider:       "alipay",
		Amount:         12_900,
		Status:         domain.RefundTransactionStatusFailed,
		FailureReason:  "支付渠道返回余额不足",
		ProcessedBy:    "user_finance_001",
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
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}

	_, err := service.RecordTransaction(context.Background(), RecordTransactionCommand{
		TenantID:         demoTenantID,
		IdempotencyKey:   testTransactionIdempotencyKey,
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
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}
	if _, err := service.ApproveRequest(context.Background(), ReviewRequestCommand{
		TenantID:   demoTenantID,
		RequestNo:  "RR202608040001",
		DecisionBy: "user_supervisor_001",
		Comment:    "订单未发货，符合退款规则",
	}); err != nil {
		t.Fatalf("ApproveRequest() error = %v", err)
	}

	_, err := service.RecordTransaction(context.Background(), RecordTransactionCommand{
		TenantID:         demoTenantID,
		IdempotencyKey:   testTransactionIdempotencyKey,
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

func TestRecordTransactionScopesProviderRefundNoByTenant(t *testing.T) {
	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())

	if _, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("demo CreateRequest() error = %v", err)
	}
	if _, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		TenantID:        acmeTenantID,
		IdempotencyKey:  testIdempotencyKey,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 25_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_acme_cs_001",
	}); err != nil {
		t.Fatalf("acme CreateRequest() error = %v", err)
	}

	for _, command := range []ReviewRequestCommand{
		{TenantID: demoTenantID, RequestNo: "RR202608040001", DecisionBy: "user_supervisor_001", Comment: "订单未发货，符合退款规则"},
		{TenantID: acmeTenantID, RequestNo: "RR202608040001", DecisionBy: "user_acme_supervisor_001", Comment: "订单未发货，符合退款规则"},
	} {
		if _, err := service.ApproveRequest(context.Background(), command); err != nil {
			t.Fatalf("ApproveRequest(%s) error = %v", command.TenantID, err)
		}
	}

	if _, err := service.RecordTransaction(context.Background(), RecordTransactionCommand{
		TenantID:         demoTenantID,
		IdempotencyKey:   testTransactionIdempotencyKey,
		RequestNo:        "RR202608040001",
		Provider:         "alipay",
		ProviderRefundNo: "ALI-SAME-REFUND-NO",
		Amount:           12_900,
		Status:           domain.RefundTransactionStatusSucceeded,
		ProcessedBy:      "user_finance_001",
	}); err != nil {
		t.Fatalf("demo RecordTransaction() error = %v", err)
	}
	if _, err := service.RecordTransaction(context.Background(), RecordTransactionCommand{
		TenantID:         acmeTenantID,
		IdempotencyKey:   testTransactionIdempotencyKey,
		RequestNo:        "RR202608040001",
		Provider:         "alipay",
		ProviderRefundNo: "ALI-SAME-REFUND-NO",
		Amount:           25_900,
		Status:           domain.RefundTransactionStatusSucceeded,
		ProcessedBy:      "user_acme_finance_001",
	}); err != nil {
		t.Fatalf("acme RecordTransaction() error = %v", err)
	}
}

func newApprovedRefundService(t *testing.T) (*Service, *InMemoryRepository) {
	t.Helper()

	now := time.Date(2026, time.August, 4, 3, 0, 0, 0, time.UTC)
	repository := NewInMemoryRepository(DemoOrders())
	service := NewService(repository, 50_000, fixedClock{now: now}, NewSequentialRequestNumberGenerator())
	if _, err := service.CreateRequest(context.Background(), CreateRequestCommand{
		TenantID:        demoTenantID,
		IdempotencyKey:  testIdempotencyKey,
		ExternalOrderNo: "LD202608040001",
		RequestedAmount: 12_900,
		ReasonCode:      "CUSTOMER_CANCELLED",
		SubmittedBy:     "user_cs_001",
	}); err != nil {
		t.Fatalf("CreateRequest() error = %v", err)
	}
	if _, err := service.ApproveRequest(context.Background(), ReviewRequestCommand{
		TenantID:   demoTenantID,
		RequestNo:  "RR202608040001",
		DecisionBy: "user_supervisor_001",
		Comment:    "订单未发货，符合退款规则",
	}); err != nil {
		t.Fatalf("ApproveRequest() error = %v", err)
	}

	return service, repository
}
