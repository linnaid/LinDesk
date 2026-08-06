package refund

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"lindesk/internal/domain"
)

// InMemoryRepository 仅用于本地演示和测试，后续会替换为真实数据库实现。
type InMemoryRepository struct {
	mutex    sync.RWMutex
	orders   map[string]domain.Order
	requests map[string]domain.RefundRequest
	// 按退款申请号分组保存审批记录，模拟 approvals 表。
	approvals map[string][]domain.Approval
	// 按退款申请号分组保存财务回填记录，模拟 refund_transactions 表。
	transactions     map[string][]domain.RefundTransaction
	providerRefundNo map[string]string
	auditLogList     []domain.AuditLog
}

func NewInMemoryRepository(orders []domain.Order) *InMemoryRepository {
	repository := &InMemoryRepository{
		orders:           make(map[string]domain.Order, len(orders)),
		requests:         make(map[string]domain.RefundRequest),
		approvals:        make(map[string][]domain.Approval),
		transactions:     make(map[string][]domain.RefundTransaction),
		providerRefundNo: make(map[string]string),
	}

	for _, order := range orders {
		repository.orders[order.ExternalOrderNo] = order
	}

	return repository
}

func DemoOrders() []domain.Order {
	paidAt := time.Date(2026, time.August, 4, 2, 30, 0, 0, time.UTC)

	return []domain.Order{
		{
			ID:                "order_1001",
			ExternalOrderNo:   "LD202608040001",
			CustomerID:        "customer_1001",
			PaymentStatus:     domain.PaymentStatusPaid,
			FulfillmentStatus: domain.FulfillmentStatusNotShipped,
			PaidAmount:        12_900,
			RefundedAmount:    0,
			Currency:          "CNY",
			PaidAt:            paidAt,
		},
		{
			ID:                "order_1002",
			ExternalOrderNo:   "LD202608040002",
			CustomerID:        "customer_1002",
			PaymentStatus:     domain.PaymentStatusPaid,
			FulfillmentStatus: domain.FulfillmentStatusShipped,
			PaidAmount:        8_800,
			RefundedAmount:    0,
			Currency:          "CNY",
			PaidAt:            paidAt,
		},
		{
			ID:                "order_1003",
			ExternalOrderNo:   "LD202608040003",
			CustomerID:        "customer_1003",
			PaymentStatus:     domain.PaymentStatusPending,
			FulfillmentStatus: domain.FulfillmentStatusNotShipped,
			PaidAmount:        6_600,
			RefundedAmount:    0,
			Currency:          "CNY",
			PaidAt:            paidAt,
		},
	}
}

func (repository *InMemoryRepository) FindOrderByExternalOrderNo(_ context.Context, externalOrderNo string) (domain.Order, error) {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()

	order, ok := repository.orders[strings.TrimSpace(externalOrderNo)]
	if !ok {
		return domain.Order{}, ErrOrderNotFound
	}

	return order, nil
}

func (repository *InMemoryRepository) CreateRefundRequest(_ context.Context, request domain.RefundRequest, auditLog domain.AuditLog) error {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()

	if _, exists := repository.requests[request.RequestNo]; exists {
		return fmt.Errorf("refund request %q already exists", request.RequestNo)
	}

	for _, existingRequest := range repository.requests {
		if existingRequest.OrderID == request.OrderID && IsActiveStatus(existingRequest.Status) {
			return ErrActiveRefundRequestExists
		}
	}

	repository.requests[request.RequestNo] = request
	if _, exists := repository.approvals[request.RequestNo]; !exists {
		repository.approvals[request.RequestNo] = nil
	}
	if _, exists := repository.transactions[request.RequestNo]; !exists {
		repository.transactions[request.RequestNo] = nil
	}
	repository.auditLogList = append(repository.auditLogList, auditLog)
	return nil
}

func (repository *InMemoryRepository) FindRefundRequestByRequestNo(_ context.Context, requestNo string) (domain.RefundRequest, error) {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()

	request, ok := repository.requests[strings.TrimSpace(requestNo)]
	if !ok {
		return domain.RefundRequest{}, ErrRefundRequestNotFound
	}

	return request, nil
}

func (repository *InMemoryRepository) ListApprovalsByRequestNo(_ context.Context, requestNo string) ([]domain.Approval, error) {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()

	if _, ok := repository.requests[strings.TrimSpace(requestNo)]; !ok {
		return nil, ErrRefundRequestNotFound
	}

	approvals := repository.approvals[strings.TrimSpace(requestNo)]
	result := make([]domain.Approval, len(approvals))
	copy(result, approvals)
	return result, nil
}

func (repository *InMemoryRepository) ReviewRefundRequest(_ context.Context, requestNo string, approval domain.Approval, requestStatus domain.RefundRequestStatus, auditLog domain.AuditLog) (domain.RefundRequest, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()

	requestNo = strings.TrimSpace(requestNo)
	request, ok := repository.requests[requestNo]
	if !ok {
		return domain.RefundRequest{}, ErrRefundRequestNotFound
	}
	if request.Status != domain.RefundRequestStatusPendingReview {
		return domain.RefundRequest{}, ErrRefundRequestNotReviewable
	}

	request.Status = requestStatus
	// 审核写入与状态更新、审计日志一起落下，模拟一次原子提交。
	repository.requests[requestNo] = request
	repository.approvals[requestNo] = append(repository.approvals[requestNo], approval)
	repository.auditLogList = append(repository.auditLogList, auditLog)

	return request, nil
}

func (repository *InMemoryRepository) ListRefundTransactionsByRequestNo(_ context.Context, requestNo string) ([]domain.RefundTransaction, error) {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()

	requestNo = strings.TrimSpace(requestNo)
	if _, ok := repository.requests[requestNo]; !ok {
		return nil, ErrRefundRequestNotFound
	}

	transactions := repository.transactions[requestNo]
	result := make([]domain.RefundTransaction, len(transactions))
	copy(result, transactions)
	return result, nil
}

func (repository *InMemoryRepository) RecordRefundTransaction(_ context.Context, requestNo string, transaction domain.RefundTransaction, requestStatus domain.RefundRequestStatus, auditLog domain.AuditLog) (domain.RefundRequest, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()

	requestNo = strings.TrimSpace(requestNo)
	request, ok := repository.requests[requestNo]
	if !ok {
		return domain.RefundRequest{}, ErrRefundRequestNotFound
	}
	if request.Status != domain.RefundRequestStatusApproved {
		return domain.RefundRequest{}, ErrRefundRequestNotApproved
	}
	if transaction.ProviderRefundNo != "" {
		if _, exists := repository.providerRefundNo[transaction.ProviderRefundNo]; exists {
			return domain.RefundRequest{}, ErrProviderRefundNoExists
		}
		repository.providerRefundNo[transaction.ProviderRefundNo] = requestNo
	}

	request.Status = requestStatus
	repository.requests[requestNo] = request
	repository.transactions[requestNo] = append(repository.transactions[requestNo], transaction)
	repository.auditLogList = append(repository.auditLogList, auditLog)

	return request, nil
}

func (repository *InMemoryRepository) AuditLogs() []domain.AuditLog {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()

	auditLogs := make([]domain.AuditLog, len(repository.auditLogList))
	copy(auditLogs, repository.auditLogList)
	return auditLogs
}
