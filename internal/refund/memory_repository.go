package refund

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"lindesk/internal/domain"
)

type InMemoryRepository struct {
	mutex        sync.RWMutex
	orders       map[string]domain.Order
	requests     map[string]domain.RefundRequest
	auditLogList []domain.AuditLog
}

func NewInMemoryRepository(orders []domain.Order) *InMemoryRepository {
	repository := &InMemoryRepository{
		orders:   make(map[string]domain.Order, len(orders)),
		requests: make(map[string]domain.RefundRequest),
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

func (repository *InMemoryRepository) AuditLogs() []domain.AuditLog {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()

	auditLogs := make([]domain.AuditLog, len(repository.auditLogList))
	copy(auditLogs, repository.auditLogList)
	return auditLogs
}
