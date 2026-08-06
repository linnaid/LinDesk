package refund

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"lindesk/internal/domain"
)

var (
	ErrOrderNotFound                = errors.New("order not found")
	ErrRefundRequestNotFound        = errors.New("refund request not found")
	ErrExternalOrderNoRequired      = errors.New("external order number is required")
	ErrRequestedAmountPositive      = errors.New("requested amount must be positive")
	ErrReasonCodeRequired           = errors.New("reason code is required")
	ErrSubmittedByRequired          = errors.New("submitted by is required")
	ErrOrderNotRefundable           = errors.New("order is not eligible for unshipped refund")
	ErrAmountExceedsRefundable      = errors.New("requested amount exceeds refundable amount")
	ErrActiveRefundRequestExists    = errors.New("active refund request exists for order")
	ErrReviewRequestNoRequired      = errors.New("refund request number is required")
	ErrApprovalDecisionByRequired   = errors.New("decision by is required")
	ErrApprovalCommentRequired      = errors.New("approval comment is required")
	ErrRefundRequestNotReviewable   = errors.New("refund request is not pending review")
	ErrApprovalActorSameAsSubmitter = errors.New("approver cannot be the submitter")
	ErrTransactionRequestNoRequired = errors.New("refund request number is required")
	ErrTransactionProviderRequired  = errors.New("refund provider is required")
	ErrProviderRefundNoRequired     = errors.New("provider refund number is required")
	ErrTransactionAmountPositive    = errors.New("refund transaction amount must be positive")
	ErrTransactionAmountMismatch    = errors.New("refund transaction amount must equal approved amount")
	ErrTransactionProcessedByNeeded = errors.New("processed by is required")
	ErrTransactionStatusInvalid     = errors.New("refund transaction status is invalid")
	ErrFailureReasonRequired        = errors.New("failure reason is required")
	ErrRefundRequestNotApproved     = errors.New("refund request is not approved")
	ErrProviderRefundNoExists       = errors.New("provider refund number already exists")
)

// Repository 定义退款闭环需要的持久化能力。
// 当前用内存仓储支撑本地演示，后续可替换为 PostgreSQL。
type Repository interface {
	FindOrderByExternalOrderNo(ctx context.Context, externalOrderNo string) (domain.Order, error)
	CreateRefundRequest(ctx context.Context, request domain.RefundRequest, auditLog domain.AuditLog) error
	FindRefundRequestByRequestNo(ctx context.Context, requestNo string) (domain.RefundRequest, error)
	// 根据退款申请编号查询所有审批记录
	ListApprovalsByRequestNo(ctx context.Context, requestNo string) ([]domain.Approval, error)
	ListRefundTransactionsByRequestNo(ctx context.Context, requestNo string) ([]domain.RefundTransaction, error)
	ReviewRefundRequest(ctx context.Context, requestNo string, approval domain.Approval, requestStatus domain.RefundRequestStatus, auditLog domain.AuditLog) (domain.RefundRequest, error)
	RecordRefundTransaction(ctx context.Context, requestNo string, transaction domain.RefundTransaction, requestStatus domain.RefundRequestStatus, auditLog domain.AuditLog) (domain.RefundRequest, error)
}

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

type RequestNumberGenerator interface {
	Next(now time.Time) string
}

type SequentialRequestNumberGenerator struct {
	mutex    sync.Mutex
	sequence int64
}

func NewSequentialRequestNumberGenerator() *SequentialRequestNumberGenerator {
	return &SequentialRequestNumberGenerator{}
}

func (generator *SequentialRequestNumberGenerator) Next(now time.Time) string {
	generator.mutex.Lock()
	defer generator.mutex.Unlock()

	generator.sequence++
	return fmt.Sprintf("RR%s%04d", now.UTC().Format("20060102"), generator.sequence)
}

type Service struct {
	repository                  Repository
	highAmountApprovalThreshold int64
	clock                       Clock
	requestNumbers              RequestNumberGenerator
}

func NewService(repository Repository, highAmountApprovalThreshold int64, clock Clock, requestNumbers RequestNumberGenerator) *Service {
	if repository == nil {
		panic("refund repository is required")
	}
	if clock == nil {
		clock = SystemClock{}
	}
	if requestNumbers == nil {
		requestNumbers = NewSequentialRequestNumberGenerator()
	}

	return &Service{
		repository:                  repository,
		highAmountApprovalThreshold: highAmountApprovalThreshold,
		clock:                       clock,
		requestNumbers:              requestNumbers,
	}
}

type CreateRequestCommand struct {
	ExternalOrderNo string
	RequestedAmount int64
	ReasonCode      string
	ReasonNote      string
	SubmittedBy     string
}

// RequestDetail 是退款详情聚合，同时携带申请本身和审批时间线。
type RequestDetail struct {
	Request                    domain.RefundRequest
	RequiresHighAmountApproval bool
	Approvals                  []domain.Approval
	RefundTransactions         []domain.RefundTransaction
}

// ReviewRequestCommand 只表达业务输入，不承载登录态；当前操作者由上层鉴权层注入。
type ReviewRequestCommand struct {
	RequestNo  string
	DecisionBy string
	Comment    string
}

type ReviewResult struct {
	Request  RequestDetail
	Approval domain.Approval
}

type RecordTransactionCommand struct {
	RequestNo        string
	Provider         string
	ProviderRefundNo string
	Amount           int64
	Status           domain.RefundTransactionStatus
	FailureReason    string
	ProcessedBy      string
}

type TransactionResult struct {
	Request     RequestDetail				// 最新退款申请详情
	Transaction domain.RefundTransaction	// 本次退款交易记录
}

func (service *Service) GetOrder(ctx context.Context, externalOrderNo string) (domain.Order, error) {
	externalOrderNo = strings.TrimSpace(externalOrderNo)
	if externalOrderNo == "" {
		return domain.Order{}, ErrExternalOrderNoRequired
	}

	return service.repository.FindOrderByExternalOrderNo(ctx, externalOrderNo)
}

// CreateRequest 先校验订单资格，再固化订单快照并进入待审状态。
func (service *Service) CreateRequest(ctx context.Context, command CreateRequestCommand) (RequestDetail, error) {
	command.ExternalOrderNo = strings.TrimSpace(command.ExternalOrderNo)
	command.ReasonCode = strings.TrimSpace(command.ReasonCode)
	command.ReasonNote = strings.TrimSpace(command.ReasonNote)
	command.SubmittedBy = strings.TrimSpace(command.SubmittedBy)

	if command.ExternalOrderNo == "" {
		return RequestDetail{}, ErrExternalOrderNoRequired
	}
	if command.RequestedAmount <= 0 {
		return RequestDetail{}, ErrRequestedAmountPositive
	}
	if command.ReasonCode == "" {
		return RequestDetail{}, ErrReasonCodeRequired
	}
	if command.SubmittedBy == "" {
		return RequestDetail{}, ErrSubmittedByRequired
	}

	order, err := service.repository.FindOrderByExternalOrderNo(ctx, command.ExternalOrderNo)
	if err != nil {
		return RequestDetail{}, err
	}
	if !order.CanRequestUnshippedRefund() {
		return RequestDetail{}, ErrOrderNotRefundable
	}
	if command.RequestedAmount > order.RefundableAmount() {
		return RequestDetail{}, ErrAmountExceedsRefundable
	}

	now := service.clock.Now().UTC()
	requestNo := service.requestNumbers.Next(now)
	request := domain.RefundRequest{
		ID:              "refund_" + requestNo,
		RequestNo:       requestNo,
		OrderID:         order.ID,
		OrderSnapshot:   order,
		RequestedAmount: command.RequestedAmount,
		ReasonCode:      command.ReasonCode,
		ReasonNote:      command.ReasonNote,
		Status:          domain.RefundRequestStatusPendingReview,
		SubmittedBy:     command.SubmittedBy,
		SubmittedAt:     now,
	}
	auditLog := domain.AuditLog{
		ID:         "audit_" + requestNo,
		EntityType: "refund_request",
		EntityID:   request.ID,
		Action:     "refund_request.created",
		OperatorID: command.SubmittedBy,
		BeforeData: map[string]any{},
		AfterData: map[string]any{
			"request_no":        request.RequestNo,
			"external_order_no": order.ExternalOrderNo,
			"requested_amount":  request.RequestedAmount,
			"status":            request.Status,
		},
		CreatedAt: now,
	}

	if err := service.repository.CreateRefundRequest(ctx, request, auditLog); err != nil {
		return RequestDetail{}, err
	}

	return service.detail(ctx, request)
}

func (service *Service) GetRequest(ctx context.Context, requestNo string) (RequestDetail, error) {
	requestNo = strings.TrimSpace(requestNo)
	if requestNo == "" {
		return RequestDetail{}, ErrRefundRequestNotFound
	}

	request, err := service.repository.FindRefundRequestByRequestNo(ctx, requestNo)
	if err != nil {
		return RequestDetail{}, err
	}

	return service.detail(ctx, request)
}

func (service *Service) ApproveRequest(ctx context.Context, command ReviewRequestCommand) (ReviewResult, error) {
	return service.reviewRequest(ctx, command, domain.ApprovalStatusApproved, domain.RefundRequestStatusApproved)
}

func (service *Service) RejectRequest(ctx context.Context, command ReviewRequestCommand) (ReviewResult, error) {
	return service.reviewRequest(ctx, command, domain.ApprovalStatusRejected, domain.RefundRequestStatusRejected)
}

// RecordTransaction 记录财务人工退款结果；只有已审批通过的申请才能结案。
func (service *Service) RecordTransaction(ctx context.Context, command RecordTransactionCommand) (TransactionResult, error) {
	command.RequestNo = strings.TrimSpace(command.RequestNo)
	command.Provider = strings.TrimSpace(command.Provider)
	command.ProviderRefundNo = strings.TrimSpace(command.ProviderRefundNo)
	command.FailureReason = strings.TrimSpace(command.FailureReason)
	command.ProcessedBy = strings.TrimSpace(command.ProcessedBy)

	if command.RequestNo == "" {
		return TransactionResult{}, ErrTransactionRequestNoRequired
	}
	if command.Provider == "" {
		return TransactionResult{}, ErrTransactionProviderRequired
	}
	if command.Amount <= 0 {
		return TransactionResult{}, ErrTransactionAmountPositive
	}
	if command.ProcessedBy == "" {
		return TransactionResult{}, ErrTransactionProcessedByNeeded
	}
	if command.Status != domain.RefundTransactionStatusSucceeded && command.Status != domain.RefundTransactionStatusFailed {
		return TransactionResult{}, ErrTransactionStatusInvalid
	}
	if command.Status == domain.RefundTransactionStatusSucceeded && command.ProviderRefundNo == "" {
		return TransactionResult{}, ErrProviderRefundNoRequired
	}
	if command.Status == domain.RefundTransactionStatusFailed && command.FailureReason == "" {
		return TransactionResult{}, ErrFailureReasonRequired
	}

	request, err := service.repository.FindRefundRequestByRequestNo(ctx, command.RequestNo)
	if err != nil {
		return TransactionResult{}, err
	}
	if request.Status != domain.RefundRequestStatusApproved {
		return TransactionResult{}, ErrRefundRequestNotApproved
	}
	if command.Amount != request.RequestedAmount {
		return TransactionResult{}, ErrTransactionAmountMismatch
	}

	now := service.clock.Now().UTC()
	transaction := domain.RefundTransaction{
		ID:               "transaction_" + request.RequestNo + "_" + strings.ToLower(string(command.Status)),
		RefundRequestID:  request.ID,
		Provider:         command.Provider,
		ProviderRefundNo: command.ProviderRefundNo,
		Amount:           command.Amount,
		Status:           command.Status,
		FailureReason:    command.FailureReason,
		ProcessedBy:      command.ProcessedBy,
		ProcessedAt:      now,
	}

	requestStatus := domain.RefundRequestStatusSucceeded
	action := "refund_request.refund_succeeded"
	if command.Status == domain.RefundTransactionStatusFailed {
		requestStatus = domain.RefundRequestStatusFailed
		action = "refund_request.refund_failed"
	}
	auditLog := domain.AuditLog{
		ID:         "audit_" + request.RequestNo + "_" + string(command.Status),
		EntityType: "refund_request",
		EntityID:   request.ID,
		Action:     action,
		OperatorID: command.ProcessedBy,
		BeforeData: map[string]any{
			"status": request.Status,
		},
		AfterData: map[string]any{
			"status":             requestStatus,
			"provider":           command.Provider,
			"provider_refund_no": command.ProviderRefundNo,
			"amount":             command.Amount,
			"transaction_status": command.Status,
			"processed_by":       command.ProcessedBy,
			"processed_at":       now,
			"failure_reason":     command.FailureReason,
		},
		CreatedAt: now,
	}

	updatedRequest, err := service.repository.RecordRefundTransaction(ctx, request.RequestNo, transaction, requestStatus, auditLog)
	if err != nil {
		return TransactionResult{}, err
	}

	detail, err := service.detail(ctx, updatedRequest)
	if err != nil {
		return TransactionResult{}, err
	}

	return TransactionResult{Request: detail, Transaction: transaction}, nil
}

// reviewRequest 让通过和驳回共享同一套校验，避免重复处理审批人、状态和备注。
func (service *Service) reviewRequest(ctx context.Context, command ReviewRequestCommand, approvalStatus domain.ApprovalStatus, requestStatus domain.RefundRequestStatus) (ReviewResult, error) {
	command.RequestNo = strings.TrimSpace(command.RequestNo)
	command.DecisionBy = strings.TrimSpace(command.DecisionBy)
	command.Comment = strings.TrimSpace(command.Comment)

	if command.RequestNo == "" {
		return ReviewResult{}, ErrReviewRequestNoRequired
	}
	if command.DecisionBy == "" {
		return ReviewResult{}, ErrApprovalDecisionByRequired
	}
	if command.Comment == "" {
		return ReviewResult{}, ErrApprovalCommentRequired
	}

	request, err := service.repository.FindRefundRequestByRequestNo(ctx, command.RequestNo)
	if err != nil {
		return ReviewResult{}, err
	}
	if request.Status != domain.RefundRequestStatusPendingReview {
		return ReviewResult{}, ErrRefundRequestNotReviewable
	}
	if request.SubmittedBy == command.DecisionBy {
		return ReviewResult{}, ErrApprovalActorSameAsSubmitter
	}

	now := service.clock.Now().UTC()
	approval := domain.Approval{
		ID:              "approval_" + request.RequestNo + "_1",
		RefundRequestID: request.ID,
		Level:           1,
		Status:          approvalStatus,
		AssigneeID:      command.DecisionBy,
		DecisionBy:      command.DecisionBy,
		DecisionAt:      &now,
		Comment:         command.Comment,
	}
	auditLog := domain.AuditLog{
		ID:         "audit_" + request.RequestNo + "_" + string(approvalStatus),
		EntityType: "refund_request",
		EntityID:   request.ID,
		Action:     "refund_request." + strings.ToLower(string(approvalStatus)),
		OperatorID: command.DecisionBy,
		BeforeData: map[string]any{
			"status": request.Status,
		},
		AfterData: map[string]any{
			"status":         requestStatus,
			"decision_by":    command.DecisionBy,
			"decision_at":    now,
			"comment":        command.Comment,
			"approval_level": approval.Level,
		},
		CreatedAt: now,
	}

	updatedRequest, err := service.repository.ReviewRefundRequest(ctx, request.RequestNo, approval, requestStatus, auditLog)
	if err != nil {
		return ReviewResult{}, err
	}

	detail, err := service.detail(ctx, updatedRequest)
	if err != nil {
		return ReviewResult{}, err
	}

	return ReviewResult{Request: detail, Approval: approval}, nil
}

// detail 查询时把审批记录一并带回，方便前端渲染完整时间线。
func (service *Service) detail(ctx context.Context, request domain.RefundRequest) (RequestDetail, error) {
	approvals, err := service.repository.ListApprovalsByRequestNo(ctx, request.RequestNo)
	if err != nil {
		return RequestDetail{}, err
	}
	transactions, err := service.repository.ListRefundTransactionsByRequestNo(ctx, request.RequestNo)
	if err != nil {
		return RequestDetail{}, err
	}

	return RequestDetail{
		Request:                    request,
		RequiresHighAmountApproval: request.RequiresHighAmountApproval(service.highAmountApprovalThreshold),
		Approvals:                  approvals,
		RefundTransactions:         transactions,
	}, nil
}

// 进行中状态用于阻止同一订单重复发起退款。
func IsActiveStatus(status domain.RefundRequestStatus) bool {
	switch status {
	case domain.RefundRequestStatusPendingReview,
		domain.RefundRequestStatusApproved,
		domain.RefundRequestStatusProcessing:
		return true
	default:
		return false
	}
}
