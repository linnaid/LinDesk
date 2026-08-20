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
	// idempotencyRecords 保存首次成功响应，模拟 PostgreSQL idempotency_records 表。
	idempotencyRecords map[string]memoryIdempotencyRecord
}

type memoryIdempotencyRecord struct {
	RequestHash string
	Request     domain.RefundRequest
}

func NewInMemoryRepository(orders []domain.Order) *InMemoryRepository {
	repository := &InMemoryRepository{
		orders:             make(map[string]domain.Order, len(orders)),
		requests:           make(map[string]domain.RefundRequest),
		approvals:          make(map[string][]domain.Approval),
		transactions:       make(map[string][]domain.RefundTransaction),
		providerRefundNo:   make(map[string]string),
		idempotencyRecords: make(map[string]memoryIdempotencyRecord),
	}

	for _, order := range orders {
		repository.orders[tenantKey(order.TenantID, order.ExternalOrderNo)] = order
	}

	return repository
}

func DemoOrders() []domain.Order {
	paidAt := time.Date(2026, time.August, 4, 2, 30, 0, 0, time.UTC)

	return []domain.Order{
		{
			ID:                "order_1001",
			TenantID:          "tenant_demo",
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
			TenantID:          "tenant_demo",
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
			TenantID:          "tenant_demo",
			ExternalOrderNo:   "LD202608040003",
			CustomerID:        "customer_1003",
			PaymentStatus:     domain.PaymentStatusPending,
			FulfillmentStatus: domain.FulfillmentStatusNotShipped,
			PaidAmount:        6_600,
			RefundedAmount:    0,
			Currency:          "CNY",
			PaidAt:            paidAt,
		},
		{
			ID:                "order_acme_1001",
			TenantID:          "tenant_acme",
			ExternalOrderNo:   "LD202608040001",
			CustomerID:        "customer_acme_1001",
			PaymentStatus:     domain.PaymentStatusPaid,
			FulfillmentStatus: domain.FulfillmentStatusNotShipped,
			PaidAmount:        25_900,
			RefundedAmount:    0,
			Currency:          "CNY",
			PaidAt:            paidAt,
		},
	}
}

func (repository *InMemoryRepository) FindOrderByExternalOrderNo(_ context.Context, tenantID string, externalOrderNo string) (domain.Order, error) {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()

	order, ok := repository.orders[tenantKey(tenantID, externalOrderNo)]
	if !ok {
		return domain.Order{}, ErrOrderNotFound
	}

	return order, nil
}

// 幂等查询
func (repository *InMemoryRepository) FindRefundRequestByIdempotency(_ context.Context, idempotency IdempotencyRecord) (domain.RefundRequest, bool, error) {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()

	existing, ok := repository.idempotencyRecords[idempotencyScopeKey(idempotency)]
	if !ok {
		return domain.RefundRequest{}, false, nil
	}
	if existing.RequestHash != idempotency.RequestHash {
		return domain.RefundRequest{}, false, ErrIdempotencyKeyConflict
	}

	return existing.Request, true, nil
}

func (repository *InMemoryRepository) CreateRefundRequest(_ context.Context, request domain.RefundRequest, auditLog domain.AuditLog, idempotency IdempotencyRecord) (CreateRequestPersistenceResult, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()

	idempotencyKey := idempotencyScopeKey(idempotency)
	if existing, ok := repository.idempotencyRecords[idempotencyKey]; ok {
		if existing.RequestHash != idempotency.RequestHash {
			return CreateRequestPersistenceResult{}, ErrIdempotencyKeyConflict
		}
		return CreateRequestPersistenceResult{Request: existing.Request, Replayed: true}, nil
	}

	key := tenantKey(request.TenantID, request.RequestNo)
	if _, exists := repository.requests[key]; exists {
		return CreateRequestPersistenceResult{}, fmt.Errorf("refund request %q already exists", request.RequestNo)
	}

	for _, existingRequest := range repository.requests {
		if existingRequest.TenantID == request.TenantID && existingRequest.OrderID == request.OrderID && IsActiveStatus(existingRequest.Status) {
			return CreateRequestPersistenceResult{}, ErrActiveRefundRequestExists
		}
	}

	repository.requests[key] = request
	if _, exists := repository.approvals[key]; !exists {
		repository.approvals[key] = nil
	}
	if _, exists := repository.transactions[key]; !exists {
		repository.transactions[key] = nil
	}
	repository.auditLogList = append(repository.auditLogList, auditLog)
	repository.idempotencyRecords[idempotencyKey] = memoryIdempotencyRecord{
		RequestHash: idempotency.RequestHash,
		Request:     request,
	}
	return CreateRequestPersistenceResult{Request: request}, nil
}

// 把一个 IdempotencyRecord 中的四个字段组成一个字符串，作为Map 的 Key
func idempotencyScopeKey(record IdempotencyRecord) string {
	return strings.Join([]string{record.TenantID, record.ActorID, record.Operation, record.Key}, "\x00")
}

func (repository *InMemoryRepository) FindRefundRequestByRequestNo(_ context.Context, tenantID string, requestNo string) (domain.RefundRequest, error) {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()

	request, ok := repository.requests[tenantKey(tenantID, requestNo)]
	if !ok {
		return domain.RefundRequest{}, ErrRefundRequestNotFound
	}

	return request, nil
}

func (repository *InMemoryRepository) ListApprovalsByRequestNo(_ context.Context, tenantID string, requestNo string) ([]domain.Approval, error) {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()

	key := tenantKey(tenantID, requestNo)
	if _, ok := repository.requests[key]; !ok {
		return nil, ErrRefundRequestNotFound
	}

	approvals := repository.approvals[key]
	result := make([]domain.Approval, len(approvals))
	copy(result, approvals)
	return result, nil
}

func (repository *InMemoryRepository) ReviewRefundRequest(_ context.Context, tenantID string, requestNo string, approval domain.Approval, requestStatus domain.RefundRequestStatus, auditLog domain.AuditLog) (domain.RefundRequest, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()

	key := tenantKey(tenantID, requestNo)
	request, ok := repository.requests[key]
	if !ok {
		return domain.RefundRequest{}, ErrRefundRequestNotFound
	}
	if request.Status != domain.RefundRequestStatusPendingReview {
		return domain.RefundRequest{}, ErrRefundRequestNotReviewable
	}

	request.Status = requestStatus
	// 审核写入与状态更新、审计日志一起落下，模拟一次原子提交。
	repository.requests[key] = request
	repository.approvals[key] = append(repository.approvals[key], approval)
	repository.auditLogList = append(repository.auditLogList, auditLog)

	return request, nil
}

func (repository *InMemoryRepository) ListRefundTransactionsByRequestNo(_ context.Context, tenantID string, requestNo string) ([]domain.RefundTransaction, error) {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()

	key := tenantKey(tenantID, requestNo)
	if _, ok := repository.requests[key]; !ok {
		return nil, ErrRefundRequestNotFound
	}

	transactions := repository.transactions[key]
	result := make([]domain.RefundTransaction, len(transactions))
	copy(result, transactions)
	return result, nil
}

func (repository *InMemoryRepository) RecordRefundTransaction(_ context.Context, tenantID string, requestNo string, transaction domain.RefundTransaction, requestStatus domain.RefundRequestStatus, auditLog domain.AuditLog) (domain.RefundRequest, error) {
	repository.mutex.Lock()
	defer repository.mutex.Unlock()

	key := tenantKey(tenantID, requestNo)
	request, ok := repository.requests[key]
	if !ok {
		return domain.RefundRequest{}, ErrRefundRequestNotFound
	}
	if request.Status != domain.RefundRequestStatusApproved {
		return domain.RefundRequest{}, ErrRefundRequestNotApproved
	}
	if transaction.ProviderRefundNo != "" {
		providerKey := tenantProviderRefundKey(tenantID, transaction.Provider, transaction.ProviderRefundNo)
		if _, exists := repository.providerRefundNo[providerKey]; exists {
			return domain.RefundRequest{}, ErrProviderRefundNoExists
		}
		repository.providerRefundNo[providerKey] = key
	}

	request.Status = requestStatus
	repository.requests[key] = request
	repository.transactions[key] = append(repository.transactions[key], transaction)
	repository.auditLogList = append(repository.auditLogList, auditLog)

	return request, nil
}

// 将租户ID和业务编号组合成一个Key
func tenantKey(tenantID, businessNo string) string {
	return strings.TrimSpace(tenantID) + "\x00" + strings.TrimSpace(businessNo)
}

// 生成 租户+支付渠道+渠道退款号 的复合唯一Key
func tenantProviderRefundKey(tenantID, provider, providerRefundNo string) string {
	return tenantKey(tenantID, strings.TrimSpace(provider)+"\x00"+strings.TrimSpace(providerRefundNo))
}

func (repository *InMemoryRepository) AuditLogs() []domain.AuditLog {
	repository.mutex.RLock()
	defer repository.mutex.RUnlock()

	auditLogs := make([]domain.AuditLog, len(repository.auditLogList))
	copy(auditLogs, repository.auditLogList)
	return auditLogs
}
